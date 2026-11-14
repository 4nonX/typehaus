package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4nonX/typehaus/internal/agent/affinitytest"
	"github.com/4nonX/typehaus/internal/protocol"
)

// stubServer answers the typehaus server's device endpoints the bridge uses.
type stubServer struct {
	mu        sync.Mutex
	activated []int64
	requests  []map[string]string
	fonts     map[string]protocol.FontStatus // PostScript → answer
}

func (st *stubServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agent/fonts/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dev-token" {
			w.WriteHeader(401)
			return
		}
		var req protocol.ResolveRequest
		json.NewDecoder(r.Body).Decode(&req)
		st.mu.Lock()
		defer st.mu.Unlock()
		out := protocol.ResolveResponse{}
		for _, f := range req.Fonts {
			s, ok := st.fonts[f.PostScript]
			if !ok {
				s = protocol.FontStatus{Status: "not_available"}
			}
			s.FontRef = f
			out.Fonts = append(out.Fonts, s)
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/agent/fonts/activate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"font_ids"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		st.mu.Lock()
		st.activated = append(st.activated, req.IDs...)
		for ps, f := range st.fonts {
			for _, id := range req.IDs {
				if f.FontID == id {
					f.Status = "active"
					st.fonts[ps] = f
				}
			}
		}
		st.mu.Unlock()
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/agent/requests", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		st.mu.Lock()
		st.requests = append(st.requests, req)
		st.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	})
	return mux
}

func (st *stubServer) activatedIDs() []int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]int64{}, st.activated...)
}

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (e *eventLog) add(kind, text string) {
	e.mu.Lock()
	e.ev = append(e.ev, kind+": "+text)
	e.mu.Unlock()
}

func (e *eventLog) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.ev...)
}

func newBridgeTest(t *testing.T, mode AffinityMode) (*AffinityBridge, *affinitytest.Fake, *stubServer, *eventLog) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TYPEHAUS_AGENT_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("TYPEHAUS_AFFINITY", string(mode))
	fake, err := affinitytest.Start("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	t.Setenv("TYPEHAUS_AFFINITY_URL", fake.URL)
	st := &stubServer{fonts: map[string]protocol.FontStatus{
		"Poppins-Regular": {Status: "available", FontID: 5, SHA256: "aaa", Family: "Poppins", Style: "Regular"},
		"Brand-Bold":      {Status: "refused", FontID: 7, SHA256: "bbb", Family: "Brand", Style: "Bold", Reason: "all 2 seats are in use"},
	}}
	srv := httptest.NewServer(st.handler(t))
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(&Config{Server: srv.URL, Token: "dev-token", DeviceID: 1, User: "dana"}, log)
	if err != nil {
		t.Fatal(err)
	}
	ev := &eventLog{}
	b := NewAffinityBridge(log, func() (*Agent, error) { return a, nil }, func() {}, ev.add, nil)
	b.interval = 50 * time.Millisecond
	return b, fake, st, ev
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fontIn(st AffinityState, doc, ps string) *AffinityFont {
	for _, d := range st.Documents {
		if d.Title != doc {
			continue
		}
		for i := range d.Fonts {
			if d.Fonts[i].PostScript == ps {
				return &d.Fonts[i]
			}
		}
	}
	return nil
}

func TestAffinityAuto(t *testing.T) {
	b, fake, st, ev := newBridgeTest(t, AffinityAuto)
	fake.SetDocs(
		affinitytest.Doc{Title: "Poster.af", Path: "C:\\Users\\dana\\Desktop\\Poster.af", Current: true, Fonts: []affinitytest.Font{
			{Name: "ArialMT", Installed: true}, {Name: "Poppins-Regular"}, {Name: "Brand-Bold"}, {Name: "Futura-Bold"}}},
		affinitytest.Doc{Title: "Flyer.af", Fonts: []affinitytest.Font{{Name: "Poppins-Regular"}}},
	)
	b.Start()
	defer b.Stop()
	waitFor(t, "connected with documents", func() bool { s := b.State(); return s.Connected && len(s.Documents) == 2 })
	// the available font is switched on once for both documents
	waitFor(t, "activation", func() bool { return len(st.activatedIDs()) == 1 })
	s := b.State()
	if ids := st.activatedIDs(); ids[0] != 5 {
		t.Fatalf("activated %v", ids)
	}
	if f := fontIn(s, "Poster.af", "ArialMT"); f == nil || !f.InAffinity || f.Status != "" {
		t.Fatalf("installed font asked about: %+v", f)
	}
	if f := fontIn(s, "Poster.af", "Brand-Bold"); f == nil || f.Status != "refused" || f.Reason == "" {
		t.Fatalf("refused font: %+v", f)
	}
	if f := fontIn(s, "Poster.af", "Futura-Bold"); f == nil || f.Status != "not_available" {
		t.Fatalf("unknown font: %+v", f)
	}
	waitFor(t, "status active", func() bool {
		f := fontIn(b.State(), "Poster.af", "Poppins-Regular")
		return f != nil && f.Status == "active"
	})
	if s.Version == "" || s.Missing() != 4 {
		t.Fatalf("version %q, missing %d", s.Version, s.Missing())
	}
	evs := strings.Join(ev.all(), "\n")
	if !strings.Contains(evs, "switched on Poppins Regular for Flyer.af, Poster.af") {
		t.Fatalf("events: %s", evs)
	}
	// Affinity picks the font up: it is no longer missing, and nothing is
	// switched on again
	fake.SetInstalled("Poppins-Regular", true)
	waitFor(t, "font found", func() bool { f := fontIn(b.State(), "Flyer.af", "Poppins-Regular"); return f != nil && f.InAffinity })
	time.Sleep(200 * time.Millisecond)
	if n := len(st.activatedIDs()); n != 1 {
		t.Fatalf("activated again: %d", n)
	}
	// the preamble is read once per session, the script each round
	calls := strings.Join(fake.Calls(), ",")
	if strings.Count(calls, "read_sdk_documentation_topic") != 1 || fake.Scripts() < 3 {
		t.Fatalf("calls: %s", calls)
	}
	// requesting the refused font
	if err := b.Request(context.Background(), "Brand-Bold", "Poster.af"); err != nil {
		t.Fatal(err)
	}
	if r := st.requests[0]; r["font"] != "Brand-Bold" || r["app"] != "Affinity" || !strings.Contains(r["purpose"], "Poster.af") {
		t.Fatalf("request: %+v", r)
	}
}

func TestAffinityShowAndReconnect(t *testing.T) {
	b, fake, st, _ := newBridgeTest(t, AffinityShow)
	fake.SetDocs(affinitytest.Doc{Title: "Brochure.af", Current: true, Fonts: []affinitytest.Font{{Name: "Poppins-Regular"}}})
	b.Start()
	defer b.Stop()
	waitFor(t, "documents", func() bool {
		f := fontIn(b.State(), "Brochure.af", "Poppins-Regular")
		return f != nil && f.Status == "available"
	})
	time.Sleep(200 * time.Millisecond)
	if n := len(st.activatedIDs()); n != 0 {
		t.Fatalf("show mode switched fonts on: %d", n)
	}
	// with a click
	if n, _ := b.ActivateMissing(context.Background(), []string{"Poppins-Regular"}); n != 1 || st.activatedIDs()[0] != 5 {
		t.Fatalf("activate: %d %v", n, st.activatedIDs())
	}
	// Affinity loses the session (messages get 404): a new one is opened
	before := fake.Connects()
	fake.ForgetSessions()
	waitFor(t, "reconnect after a lost session", func() bool { return fake.Connects() > before && b.State().Connected })
	// Affinity quits and starts again
	before = fake.Connects()
	fake.Restart()
	waitFor(t, "reconnect after a restart", func() bool { return fake.Connects() > before && b.State().Connected })
	// each new session reads the preamble before scripts work again
	if strings.Count(strings.Join(fake.Calls(), ","), "read_sdk_documentation_topic") < 3 {
		t.Fatalf("preamble not read per session: %v", fake.Calls())
	}
	// switched off: no connection at all
	if err := b.SetMode(AffinityOff); err != nil {
		t.Fatal(err)
	}
	if s := b.State(); s.Connected || len(s.Documents) != 0 || s.Mode != AffinityOff {
		t.Fatalf("off: %+v", s)
	}
	if LoadAffinityMode() != AffinityShow { // the environment wins over the stored setting
		t.Fatal("TYPEHAUS_AFFINITY not honoured")
	}
	os.Unsetenv("TYPEHAUS_AFFINITY")
	if LoadAffinityMode() != AffinityOff {
		t.Fatal("setting not stored")
	}
}

func TestAffinityNotRunning(t *testing.T) {
	b, fake, _, _ := newBridgeTest(t, AffinityAuto)
	fake.Close()
	b.Start()
	defer b.Stop()
	waitFor(t, "a message", func() bool { return b.State().Message != "" })
	if s := b.State(); s.Connected || !strings.Contains(s.Message, "Enable MCP server") {
		t.Fatalf("%+v", s)
	}
}

func TestParseScriptDocs(t *testing.T) {
	out := "warning: something\nN1:{\"ok\":true,\"version\":\"3.3\",\"docs\":[{\"title\":\"A\",\"path\":\"\",\"current\":true,\"fonts\":[[\"B\",true],[\"A\",false],[\"A\",false]]}]}\n"
	docs, v, err := parseScriptDocs(out, "N1:")
	if err != nil || v != "3.3" || len(docs) != 1 || len(docs[0].Fonts) != 2 || docs[0].Fonts[0].PostScript != "A" {
		t.Fatalf("%v %q %+v", err, v, docs)
	}
	if _, _, err := parseScriptDocs("TypeError: x is not a function\n    at main:3:9\n", "N1:"); err == nil || !strings.Contains(err.Error(), "TypeError") {
		t.Fatalf("error not reported: %v", err)
	}
	if _, _, err := parseScriptDocs(`N1:{"ok":false,"error":"INVALID_OP"}`, "N1:"); err == nil || !strings.Contains(err.Error(), "INVALID_OP") {
		t.Fatalf("script error: %v", err)
	}
}

// The font script itself runs in Node against stand-ins for Affinity's SDK
// modules, the way Affinity runs it (as the body of a function).
func TestAffinityScriptInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	harness := `
const vm = require('vm');
const script = require('fs').readFileSync(0, 'utf8');
const out = [];
const log = (...a) => out.push(a.map(String).join(' '));
// Affinity's SDK objects: documents with enumerateFontNames; a truthy return
// from the callback ends the enumeration (as measured).
function doc(title, path, uuid, fonts) {
  return { title, path, sessionUuid: { toString: () => uuid },
    enumerateFontNames(cb) { for (const [n, i] of fonts) { if (cb(n, i)) break; } } };
}
const docs = [doc('Poster', 'C:\\Users\\dana\\Poster.af', 'U1', [['ArialMT', true], ['Poppins-Regular', false], ['Brand-Bold', false]]),
              doc('Untitled', '', 'U2', [])];
const app = { version: 'Affinity 3.3.1.4940', documents: { current: docs[0], all: docs } };
const require2 = (m) => { if (m === '/application.js') return { app }; throw new Error("Cannot find module '" + m + "'"); };
try {
  vm.runInNewContext('(function main(){' + script + '\n})()', { console: { log, info: log, warn: log, error: log }, require: require2 }, { timeout: 5000 });
} catch (e) { out.push(e.name + ': ' + e.message); }
process.stdout.write(out.join('\n') + '\n');
`
	dir := t.TempDir()
	h := filepath.Join(dir, "harness.js")
	os.WriteFile(h, []byte(harness), 0o600)
	nonce := newNonce()
	cmd := exec.Command(node, h)
	cmd.Stdin = strings.NewReader(strings.Replace(affinityScript, "__NONCE__", nonce, 1))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	docs, v, err := parseScriptDocs(string(out), nonce)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if v != "Affinity 3.3.1.4940" || len(docs) != 2 || !docs[0].Current || docs[1].Current || docs[0].Path == "" {
		t.Fatalf("%q %+v", v, docs)
	}
	got := map[string]bool{}
	for _, f := range docs[0].Fonts {
		got[f.PostScript] = f.InAffinity
	}
	if len(got) != 3 || !got["ArialMT"] || got["Poppins-Regular"] || got["Brand-Bold"] {
		t.Fatalf("fonts: %+v", docs[0].Fonts)
	}
	// a broken SDK is reported, not mistaken for "no documents"
	cmd = exec.Command(node, h)
	cmd.Stdin = strings.NewReader(strings.Replace(strings.Replace(affinityScript, "__NONCE__", nonce, 1), "/application.js", "/missing.js", 1))
	out, _ = cmd.CombinedOutput()
	if _, _, err := parseScriptDocs(string(out), nonce); err == nil || !strings.Contains(err.Error(), "Cannot find module") {
		t.Fatalf("SDK error not reported: %v\n%s", err, out)
	}
}

// FuzzParseScriptDocs: what Affinity's script console prints comes from
// the open documents – font names included.
func FuzzParseScriptDocs(f *testing.F) {
	f.Add("warning: something\nN1:{\"ok\":true,\"version\":\"3.3\",\"docs\":[{\"title\":\"A\",\"path\":\"\",\"current\":true,\"fonts\":[[\"B\",true],[\"A\",false]]}]}\n", "N1:")
	f.Add("TypeError: x is not a function\n    at main:3:9\n", "N1:")
	f.Add(`N1:{"ok":false,"error":"INVALID_OP"}`, "N1:")
	f.Fuzz(func(t *testing.T, out, nonce string) {
		parseScriptDocs(out, nonce)
	})
}
