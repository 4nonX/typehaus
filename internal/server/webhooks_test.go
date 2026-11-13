package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/4nonX/typehaus/internal/protocol"
)

type received struct {
	event, delivery string
	body            webhookEvent
}

// sink is a webhook receiver that checks signatures like an integration
// would; fail makes it answer 500 for the first n deliveries.
type sink struct {
	mu     sync.Mutex
	got    []received
	secret string
	fail   int
	t      *testing.T
}

func (k *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	ts, _ := strconv.ParseInt(r.Header.Get("X-Typehaus-Timestamp"), 10, 64)
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.secret != "" && r.Header.Get("X-Typehaus-Signature") != signature(k.secret, ts, body) {
		k.t.Errorf("bad signature for %s", r.Header.Get("X-Typehaus-Event"))
	}
	if k.fail > 0 {
		k.fail--
		w.WriteHeader(500)
		return
	}
	var ev webhookEvent
	json.Unmarshal(body, &ev)
	if ev.Type != r.Header.Get("X-Typehaus-Event") {
		k.t.Errorf("event header %q, body %q", r.Header.Get("X-Typehaus-Event"), ev.Type)
	}
	k.got = append(k.got, received{ev.Type, r.Header.Get("X-Typehaus-Delivery"), ev})
}

func (k *sink) types() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, g := range k.got {
		out = append(out, g.event)
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestWebhooks(t *testing.T) {
	old := webhookBackoff
	webhookBackoff = []time.Duration{0, 0}
	defer func() { webhookBackoff = old }()

	s, ts, admin := newTestServer(t)
	all, lics := &sink{t: t}, &sink{t: t}
	allSrv, licSrv := httptest.NewServer(all), httptest.NewServer(lics)
	defer allSrv.Close()
	defer licSrv.Close()

	// validation
	if st := admin.do("POST", "/api/webhooks", map[string]any{"name": "Meta", "url": "http://169.254.169.254/latest"}, nil); st != 400 {
		t.Fatalf("metadata address: %d", st)
	}
	if st := admin.do("POST", "/api/webhooks", map[string]any{"name": "X", "url": allSrv.URL, "events": []string{"nope.event"}}, nil); st != 400 {
		t.Fatalf("unknown event: %d", st)
	}
	var h1, h2 webhookCreated
	admin.do("POST", "/api/webhooks", map[string]any{"name": "Everything", "url": allSrv.URL}, &h1)
	admin.do("POST", "/api/webhooks", map[string]any{"name": "Licenses", "url": licSrv.URL, "events": []string{"license.*"}}, &h2)
	all.secret, lics.secret = h1.Secret, h2.Secret

	// a read token can't manage webhooks
	ro := newToken(t, admin, "RO", "read")
	if st, _, _ := apiCall(t, ts.URL, ro, "POST", "/api/v1/webhooks", map[string]any{"name": "x", "url": allSrv.URL}); st != 403 {
		t.Fatalf("read token creating a webhook: %d", st)
	}

	// events from ordinary actions
	var lic, col map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Brand", "kind": "commercial", "expires_on": time.Now().AddDate(0, 0, 5).Format("2006-01-02")}, &lic)
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"seats": 3}, nil)
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	admin.do("POST", "/api/users", map[string]any{"username": "gil", "password": "gilpassword1"}, nil)
	var req map[string]int64
	admin.do("POST", "/api/requests", map[string]any{"font": "Futura"}, &req)
	admin.do("PATCH", "/api/requests/"+itoa(req["id"]), map[string]any{"status": "done", "answer": "Added"}, nil)
	enrollDevice(t, ts, admin)

	want := []string{"license.created", "license.updated", "collection.created", "font.added", "user.created", "request.created", "request.answered", "device.enrolled"}
	waitFor(t, "all events", func() bool {
		got := all.types()
		for _, w := range want {
			if !contains(got, w) {
				return false
			}
		}
		return true
	})
	waitFor(t, "license events", func() bool { return len(lics.types()) >= 2 })
	for _, ty := range lics.types() {
		if ty != "license.created" && ty != "license.updated" {
			t.Fatalf("license.* webhook got %s", ty)
		}
	}
	// payload: the license as the API shows it
	for _, g := range all.got {
		if g.event == "license.updated" {
			d := g.body.Data.(map[string]any)
			if d["license"].(map[string]any)["seats"].(float64) != 3 || g.body.ID == "" || g.body.CreatedAt == 0 {
				t.Fatalf("license.updated payload: %+v", g.body)
			}
		}
	}

	// scheduled: license expiring in 5 days → once (the 7-day step)
	s.scheduledEvents()
	s.scheduledEvents()
	waitFor(t, "license.expiring", func() bool { return contains(lics.types(), "license.expiring") })
	n := 0
	for _, ty := range lics.types() {
		if ty == "license.expiring" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("license.expiring sent %d times", n)
	}

	// retries: two failures, then delivered on the third attempt
	all.mu.Lock()
	all.fail = 2
	before := len(all.got)
	all.mu.Unlock()
	admin.do("POST", "/api/webhooks/"+itoa(h1.ID)+"/test", nil, nil)
	waitFor(t, "retried test delivery", func() bool { all.mu.Lock(); defer all.mu.Unlock(); return len(all.got) > before })
	ds := waitDelivered(t, admin, h1.ID)
	if ds[0].Event != "webhook.test" || ds[0].Status != "delivered" || ds[0].Attempts != 3 {
		t.Fatalf("retried delivery: %+v", ds[0])
	}

	// gives up after the last retry, can be sent again by hand
	all.mu.Lock()
	all.fail = 10
	all.mu.Unlock()
	admin.do("POST", "/api/webhooks/"+itoa(h1.ID)+"/test", nil, nil)
	ds = waitDelivered(t, admin, h1.ID)
	if ds[0].Status != "failed" || ds[0].LastCode == nil || *ds[0].LastCode != 500 {
		t.Fatalf("failed delivery: %+v", ds[0])
	}
	all.mu.Lock()
	all.fail = 0
	all.mu.Unlock()
	if st := admin.do("POST", "/api/webhooks/"+itoa(h1.ID)+"/deliveries/"+itoa(ds[0].ID)+"/redeliver", nil, nil); st != 202 {
		t.Fatalf("redeliver: %d", st)
	}
	ds = waitDelivered(t, admin, h1.ID)
	if ds[0].Status != "delivered" {
		t.Fatalf("after redeliver: %+v", ds[0])
	}

	// new secret: old signatures stop matching
	var rot webhookCreated
	admin.do("POST", "/api/webhooks/"+itoa(h1.ID)+"/secret", nil, &rot)
	if rot.Secret == h1.Secret || rot.Secret == "" {
		t.Fatal("secret not rotated")
	}
	all.mu.Lock()
	all.secret = rot.Secret
	all.mu.Unlock()
	admin.do("POST", "/api/webhooks/"+itoa(h1.ID)+"/test", nil, nil)
	waitDelivered(t, admin, h1.ID)

	// inactive webhooks receive nothing
	admin.do("PATCH", "/api/webhooks/"+itoa(h2.ID), map[string]any{"active": false}, nil)
	k := len(lics.types())
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"seats": 4}, nil)
	time.Sleep(300 * time.Millisecond)
	if len(lics.types()) != k {
		t.Fatal("inactive webhook got an event")
	}
}

func TestWebhookInventoryAndTokenEvents(t *testing.T) {
	s, ts, admin := newTestServer(t)
	s.cfg.Inventory = true
	k := &sink{t: t}
	srv := httptest.NewServer(k)
	defer srv.Close()
	var h webhookCreated
	admin.do("POST", "/api/webhooks", map[string]any{"name": "Compliance", "url": srv.URL, "events": []string{"inventory.copy_found", "token.expiring"}}, &h)
	k.secret = h.Secret

	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Paid", "kind": "commercial"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{fonts[0].ID}, "license_id": lic["id"]}, nil)
	dev := enrollDevice(t, ts, admin)
	report := []protocol.InventoryFont{{Path: "/Library/Fonts/Poppins.ttf", Scope: "computer", Family: "Poppins", Style: "Regular", SHA256: fonts[0].SHA256, Size: 1}}
	sendInventory(t, ts.URL, dev, report)
	sendInventory(t, ts.URL, dev, report) // the same copy again: no second event
	waitFor(t, "copy_found", func() bool { return contains(k.types(), "inventory.copy_found") })

	admin.do("POST", "/api/tokens", map[string]any{"name": "Short", "scope": "read", "expires_days": 3}, nil)
	s.scheduledEvents()
	waitFor(t, "token.expiring", func() bool { return contains(k.types(), "token.expiring") })
	time.Sleep(200 * time.Millisecond)
	n := 0
	for _, ty := range k.types() {
		if ty == "inventory.copy_found" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("copy_found sent %d times", n)
	}
	for _, g := range k.got {
		if g.event == "inventory.copy_found" {
			c := g.body.Data.(map[string]any)["copies"].([]any)[0].(map[string]any)
			if c["license"] != "Paid" || c["copy"].(map[string]any)["exact"] != true {
				t.Fatalf("copy_found payload: %+v", c)
			}
		}
	}
}
