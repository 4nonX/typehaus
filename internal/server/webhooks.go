package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- webhooks ------------------------------------------------------------------
//
// Administrators register URLs that receive events as signed JSON POSTs:
//
//	X-Typehaus-Event:     license.expiring
//	X-Typehaus-Delivery:  delivery id (the same on retries)
//	X-Typehaus-Timestamp: unix seconds
//	X-Typehaus-Signature: v1=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//
// Deliveries that fail (no 2xx within 10 s) are retried with growing gaps for
// about 22 hours. Event types and payloads are part of API v1.

// webhookEvents is the catalog: event type → description. Part of the API
// contract: types are only ever added.
var webhookEvents = map[string]string{
	"font.added":           "Fonts were added to the library.",
	"font.deleted":         "Fonts were deleted from the library.",
	"font.license_changed": "Fonts were assigned to a license (or unassigned).",
	"collection.created":   "A collection was created.",
	"collection.updated":   "A collection's name, audience, mode or fonts changed.",
	"collection.deleted":   "A collection was deleted.",
	"license.created":      "A license was created.",
	"license.updated":      "A license was changed.",
	"license.deleted":      "A license was deleted.",
	"license.expiring":     "A license expires in 30, 14, 7 or 1 day(s) (sent once per step).",
	"license.over_seats":   "More people or computers use a license than it has seats (at most once a day).",
	"request.created":      "Someone requested a font.",
	"request.answered":     "An administrator answered a font request.",
	"inventory.copy_found": "A copy of a licensed library font was found installed outside typehaus.",
	"device.enrolled":      "A computer was connected.",
	"device.removed":       "A computer was disconnected.",
	"user.created":         "A user account was created.",
	"user.updated":         "A user account was changed (e.g. disabled, admin rights).",
	"user.deleted":         "A user account was deleted.",
	"token.expiring":       "An API token expires in 7 or 1 day(s) (sent once per step).",
	"webhook.test":         "Sent by Send test.",
}

// webhookEvent is the body of every delivery.
type webhookEvent struct {
	ID        string `json:"id"`         // evt_…, unique per event
	Type      string `json:"type"`       // see the catalog
	CreatedAt int64  `json:"created_at"` // unix seconds
	Data      any    `json:"data"`
}

type webhookRow struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Events    []string `json:"events"` // ["*"] = all
	Active    bool     `json:"active"`
	CreatedAt int64    `json:"created_at"`
	// recent health
	LastStatus   string `json:"last_status"` // delivered | failed | pending | ""
	LastAt       *int64 `json:"last_at"`
	FailedRecent int    `json:"failed_recent"` // failed deliveries, last 7 days
}

type webhookReq struct {
	Name   *string   `json:"name"`
	URL    *string   `json:"url"`
	Events *[]string `json:"events"`
	Active *bool     `json:"active"`
}

type webhookDelivery struct {
	ID          int64  `json:"id"`
	Event       string `json:"event"`
	EventID     string `json:"event_id"`
	Status      string `json:"status"` // pending | delivered | failed
	Attempts    int    `json:"attempts"`
	LastCode    *int   `json:"last_code"`
	LastError   string `json:"last_error"`
	CreatedAt   int64  `json:"created_at"`
	DeliveredAt *int64 `json:"delivered_at"`
	NextAt      *int64 `json:"next_at"`
}

// retry gaps after the 1st, 2nd … failed attempt (about 22 hours in all)
var webhookBackoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// emit queues an event for every active webhook that subscribed to it.
func (s *Server) emit(typ string, data any) {
	if _, ok := webhookEvents[typ]; !ok {
		s.log.Error("unknown webhook event", "type", typ)
		return
	}
	ev := webhookEvent{ID: "evt_" + randomToken(12), Type: typ, CreatedAt: now(), Data: data}
	body, err := json.Marshal(ev)
	if err != nil {
		s.log.Error("webhook payload", "err", err)
		return
	}
	rows, err := s.db.Query(`SELECT id, events FROM webhooks WHERE active = 1`)
	if err != nil {
		return
	}
	var targets []int64
	for rows.Next() {
		var id int64
		var events string
		rows.Scan(&id, &events)
		if subscribed(events, typ) {
			targets = append(targets, id)
		}
	}
	rows.Close()
	for _, id := range targets {
		s.db.Exec(`INSERT INTO webhook_deliveries (webhook_id, event, event_id, payload, next_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, typ, ev.ID, string(body), now(), now())
	}
	if len(targets) > 0 {
		s.wakeWebhooks()
	}
}

func subscribed(events, typ string) bool {
	for _, e := range strings.Split(events, ",") {
		e = strings.TrimSpace(e)
		if e == "*" || e == typ || (strings.HasSuffix(e, ".*") && strings.HasPrefix(typ, strings.TrimSuffix(e, "*"))) {
			return true
		}
	}
	return false
}

func (s *Server) wakeWebhooks() {
	select {
	case s.webhookWake <- struct{}{}:
	default:
	}
}

// webhookWorker delivers due events until ctx ends.
func (s *Server) webhookWorker(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // a redirect is not a delivery
	}}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		s.deliverDue(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.webhookWake:
		}
	}
}

func (s *Server) deliverDue(ctx context.Context, client *http.Client) {
	type job struct {
		id, hook       int64
		event, payload string
		attempts       int
		url, secret    string
	}
	rows, err := s.db.Query(`SELECT d.id, d.webhook_id, d.event, d.payload, d.attempts, w.url, w.secret
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.status = 'pending' AND d.next_at <= ? AND w.active = 1 ORDER BY d.id LIMIT 25`, now())
	if err != nil {
		return
	}
	var jobs []job
	for rows.Next() {
		var j job
		rows.Scan(&j.id, &j.hook, &j.event, &j.payload, &j.attempts, &j.url, &j.secret)
		jobs = append(jobs, j)
	}
	rows.Close()
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		code, err := s.deliver(ctx, client, j.url, j.secret, j.event, j.id, []byte(j.payload))
		attempts := j.attempts + 1
		var codeV any
		if code > 0 {
			codeV = code
		}
		if err == nil {
			s.db.Exec(`UPDATE webhook_deliveries SET status = 'delivered', attempts = ?, last_code = ?, last_error = '', delivered_at = ? WHERE id = ?`,
				attempts, codeV, now(), j.id)
			continue
		}
		if attempts > len(webhookBackoff) {
			s.db.Exec(`UPDATE webhook_deliveries SET status = 'failed', attempts = ?, last_code = ?, last_error = ? WHERE id = ?`,
				attempts, codeV, trunc(err.Error(), 500), j.id)
			s.log.Warn("webhook delivery gave up", "webhook", j.hook, "event", j.event, "err", err)
			continue
		}
		next := time.Now().Add(webhookBackoff[attempts-1]).Unix()
		s.db.Exec(`UPDATE webhook_deliveries SET attempts = ?, last_code = ?, last_error = ?, next_at = ? WHERE id = ?`,
			attempts, codeV, trunc(err.Error(), 500), next, j.id)
		if next <= now() {
			s.wakeWebhooks() // already due again
		}
	}
}

// signature is what receivers recompute to verify a delivery.
func signature(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

func (s *Server) deliver(ctx context.Context, client *http.Client, target, secret, event string, deliveryID int64, body []byte) (int, error) {
	if err := checkWebhookURL(target); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	ts := now()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "typehaus/"+Version)
	req.Header.Set("X-Typehaus-Event", event)
	req.Header.Set("X-Typehaus-Delivery", strconv.FormatInt(deliveryID, 10))
	req.Header.Set("X-Typehaus-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Typehaus-Signature", signature(secret, ts, body))
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("receiver answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// checkWebhookURL: http(s) only, and never the cloud metadata addresses
// (169.254.0.0/16, fd00:ec2::254) – internal servers are allowed, they are
// what a self-hosted typehaus usually talks to.
func checkWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the URL must start with http:// or https://")
	}
	host := u.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			return nil // resolved at delivery time; a failure is retried
		}
	}
	for _, ip := range ips {
		if ip.IsLinkLocalUnicast() || ip.Equal(net.ParseIP("fd00:ec2::254")) {
			return errors.New("link-local and cloud metadata addresses are not allowed")
		}
	}
	return nil
}

// ---- scheduled events ------------------------------------------------------------

// once reports whether key is new (and remembers it): scheduled events are
// sent once per step.
func (s *Server) once(key string) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO webhook_once (key, created_at) VALUES (?, ?)`, key, now())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// scheduledEvents runs from the janitor: expiring and over-used licenses,
// expiring tokens.
func (s *Server) scheduledEvents() {
	var hooks int
	s.db.QueryRow(`SELECT COUNT(*) FROM webhooks WHERE active = 1`).Scan(&hooks)
	if hooks == 0 {
		return
	}
	ls, err := s.queryLicenses(&authUser{IsAdmin: true}, "")
	if err != nil {
		return
	}
	today := usageDay(time.Now())
	for _, l := range ls {
		if l.ExpiresIn != nil && !l.Expired {
			for _, step := range []int{1, 7, 14, 30} {
				if *l.ExpiresIn <= step {
					if s.once(fmt.Sprintf("license.expiring:%d:%s:%d", l.ID, l.ExpiresOn, step)) {
						s.emit("license.expiring", map[string]any{"license": l, "days_left": *l.ExpiresIn})
					}
					break
				}
			}
		}
		if l.Over && s.once(fmt.Sprintf("license.over_seats:%d:%s", l.ID, today)) {
			s.emit("license.over_seats", map[string]any{"license": l, "used": l.Used, "seats": l.Seats})
		}
	}
	rows, err := s.db.Query(`SELECT id, name, expires_at FROM api_tokens WHERE expires_at IS NOT NULL AND expires_at > ?`, now())
	if err != nil {
		return
	}
	type tok struct {
		id      int64
		name    string
		expires int64
	}
	var toks []tok
	for rows.Next() {
		var t tok
		rows.Scan(&t.id, &t.name, &t.expires)
		toks = append(toks, t)
	}
	rows.Close()
	for _, t := range toks {
		days := int((t.expires - now()) / 86400)
		for _, step := range []int{1, 7} {
			if days < step && s.once(fmt.Sprintf("token.expiring:%d:%d:%d", t.id, t.expires, step)) {
				s.emit("token.expiring", map[string]any{"token": map[string]any{"id": t.id, "name": t.name, "expires_at": t.expires}, "days_left": days})
				break
			}
		}
	}
}

// ---- handlers ------------------------------------------------------------------

func (s *Server) webhookList(where string, args ...any) ([]webhookRow, error) {
	q := `SELECT w.id, w.name, w.url, w.events, w.active, w.created_at,
		(SELECT d.status FROM webhook_deliveries d WHERE d.webhook_id = w.id AND d.status != 'pending' ORDER BY d.id DESC LIMIT 1),
		(SELECT MAX(COALESCE(d.delivered_at, d.created_at)) FROM webhook_deliveries d WHERE d.webhook_id = w.id AND d.status != 'pending'),
		(SELECT COUNT(*) FROM webhook_deliveries d WHERE d.webhook_id = w.id AND d.status = 'failed' AND d.created_at > ?)
		FROM webhooks w`
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := s.db.Query(q+" ORDER BY w.name COLLATE NOCASE", append([]any{now() - 7*86400}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []webhookRow{}
	for rows.Next() {
		var h webhookRow
		var events string
		var last sql.NullString
		var lastAt sql.NullInt64
		if err := rows.Scan(&h.ID, &h.Name, &h.URL, &events, &h.Active, &h.CreatedAt, &last, &lastAt, &h.FailedRecent); err != nil {
			return nil, err
		}
		h.Events = strings.Split(events, ",")
		h.LastStatus = last.String
		if lastAt.Valid {
			h.LastAt = &lastAt.Int64
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request, u *authUser) {
	out, err := s.webhookList("")
	if err != nil {
		s.internalErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	out, err := s.webhookList("w.id = ?", id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if len(out) == 0 {
		writeErr(w, 404, "webhook not found")
		return
	}
	writeJSON(w, 200, out[0])
}

// validate checks a webhook request and returns the columns to set.
func (req *webhookReq) columns() (map[string]any, error) {
	c := map[string]any{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 100 {
			return nil, errors.New("name required (max. 100 characters)")
		}
		c["name"] = n
	}
	if req.URL != nil {
		u := strings.TrimSpace(*req.URL)
		if len(u) > 2000 {
			return nil, errors.New("the URL is too long")
		}
		if err := checkWebhookURL(u); err != nil {
			return nil, err
		}
		c["url"] = u
	}
	if req.Events != nil {
		if len(*req.Events) == 0 {
			return nil, errors.New("choose at least one event (or \"*\" for all)")
		}
		for _, e := range *req.Events {
			_, known := webhookEvents[e]
			family := strings.HasSuffix(e, ".*")
			if e != "*" && !known && !family {
				return nil, fmt.Errorf("unknown event %q", e)
			}
		}
		c["events"] = strings.Join(*req.Events, ",")
	}
	if req.Active != nil {
		c["active"] = boolInt(*req.Active)
	}
	return c, nil
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req webhookReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == nil || req.URL == nil {
		writeErr(w, 400, "name and url required")
		return
	}
	if req.Events == nil {
		req.Events = &[]string{"*"}
	}
	c, err := req.columns()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	secret := "whsec_" + randomToken(24)
	res, err := s.db.Exec(`INSERT INTO webhooks (name, url, secret, events, active, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c["name"], c["url"], secret, c["events"], boolInt(req.Active == nil || *req.Active), u.ID, now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(u, "webhook.create", "%s → %s", c["name"], c["url"])
	// the signing secret is shown once; rotate it to get a new one
	writeJSON(w, 201, webhookCreated{ID: id, Secret: secret})
}

type webhookCreated struct {
	ID     int64  `json:"id"`
	Secret string `json:"secret"` // shown once – used to verify X-Typehaus-Signature
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("webhooks", id) {
		writeErr(w, 404, "webhook not found")
		return
	}
	var req webhookReq
	if !readJSON(w, r, &req) {
		return
	}
	c, err := req.columns()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	for col, v := range c {
		s.db.Exec(`UPDATE webhooks SET `+col+` = ? WHERE id = ?`, v, id) // col from a fixed set
	}
	s.audit(u, "webhook.update", "webhook %d", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM webhooks WHERE id = ?`, id).Scan(&name); err != nil {
		writeErr(w, 404, "webhook not found")
		return
	}
	s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	s.audit(u, "webhook.delete", "%s", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("webhooks", id) {
		writeErr(w, 404, "webhook not found")
		return
	}
	secret := "whsec_" + randomToken(24)
	s.db.Exec(`UPDATE webhooks SET secret = ? WHERE id = ?`, secret, id)
	s.audit(u, "webhook.secret", "webhook %d", id)
	writeJSON(w, 200, webhookCreated{ID: id, Secret: secret})
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("webhooks", id) {
		writeErr(w, 404, "webhook not found")
		return
	}
	ev := webhookEvent{ID: "evt_" + randomToken(12), Type: "webhook.test", CreatedAt: now(),
		Data: map[string]any{"message": "Hello from typehaus", "sent_by": u.Username}}
	body, _ := json.Marshal(ev)
	res, err := s.db.Exec(`INSERT INTO webhook_deliveries (webhook_id, event, event_id, payload, next_at, created_at) VALUES (?, 'webhook.test', ?, ?, ?, ?)`,
		id, ev.ID, string(body), now(), now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	did, _ := res.LastInsertId()
	s.wakeWebhooks()
	writeJSON(w, 202, apiCreated{ID: did})
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("webhooks", id) {
		writeErr(w, 404, "webhook not found")
		return
	}
	rows, err := s.db.Query(`SELECT id, event, event_id, status, attempts, last_code, last_error, created_at, delivered_at, next_at
		FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 1000`, id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []webhookDelivery{}
	for rows.Next() {
		var d webhookDelivery
		var code, delivered, next sql.NullInt64
		rows.Scan(&d.ID, &d.Event, &d.EventID, &d.Status, &d.Attempts, &code, &d.LastError, &d.CreatedAt, &delivered, &next)
		if code.Valid {
			c := int(code.Int64)
			d.LastCode = &c
		}
		if delivered.Valid {
			d.DeliveredAt = &delivered.Int64
		}
		if next.Valid && d.Status == "pending" {
			d.NextAt = &next.Int64
		}
		out = append(out, d)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleRedeliver(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	did, ok2 := strconv.ParseInt(r.PathValue("delivery"), 10, 64)
	if !ok || ok2 != nil {
		writeErr(w, 400, "bad id")
		return
	}
	res, err := s.db.Exec(`UPDATE webhook_deliveries SET status = 'pending', attempts = 0, next_at = ?, last_error = '' WHERE id = ? AND webhook_id = ?`,
		now(), did, id)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "delivery not found")
		return
	}
	s.wakeWebhooks()
	s.audit(u, "webhook.redeliver", "webhook %d, delivery %d", id, did)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

type webhookEventInfo struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (s *Server) handleWebhookEvents(w http.ResponseWriter, r *http.Request, u *authUser) {
	out := []webhookEventInfo{}
	for _, t := range sortedEventTypes() {
		out = append(out, webhookEventInfo{t, webhookEvents[t]})
	}
	writeJSON(w, 200, out)
}

func sortedEventTypes() []string {
	var out []string
	for t := range webhookEvents {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// eventFont is how events describe a font.
type eventFont struct {
	ID         int64  `json:"id"`
	Family     string `json:"family"`
	Style      string `json:"style"`
	FullName   string `json:"full_name"`
	PostScript string `json:"postscript_name"`
	LicenseID  *int64 `json:"license_id"`
}

func (s *Server) eventFonts(ids []int64) []eventFont {
	out := []eventFont{}
	if len(ids) == 0 {
		return out
	}
	rows, err := s.db.Query(`SELECT id, family, style, full_name, postscript_name, license_id FROM fonts WHERE id IN (`+placeholders(len(ids))+`) ORDER BY family, style`, int64Args(ids)...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var f eventFont
		var lic sql.NullInt64
		rows.Scan(&f.ID, &f.Family, &f.Style, &f.FullName, &f.PostScript, &lic)
		if lic.Valid {
			f.LicenseID = &lic.Int64
		}
		out = append(out, f)
	}
	return out
}

// eventLicense is the license as the API shows it (nil once deleted).
func (s *Server) eventLicense(id int64) *licenseRow {
	ls, err := s.queryLicenses(&authUser{IsAdmin: true}, "l.id = ?", id)
	if err != nil || len(ls) == 0 {
		return nil
	}
	return ls[0]
}

// eventCollection is the collection as GET /collections shows it.
func (s *Server) eventCollection(id int64) map[string]any {
	var name, desc, mode string
	var everyone bool
	if err := s.db.QueryRow(`SELECT name, description, mode, everyone FROM collections WHERE id = ?`, id).Scan(&name, &desc, &mode, &everyone); err != nil {
		return map[string]any{"id": id}
	}
	groups := []int64{}
	if rows, err := s.db.Query(`SELECT group_id FROM collection_groups WHERE collection_id = ?`, id); err == nil {
		for rows.Next() {
			var g int64
			rows.Scan(&g)
			groups = append(groups, g)
		}
		rows.Close()
	}
	var fonts int
	s.db.QueryRow(`SELECT COUNT(*) FROM collection_fonts WHERE collection_id = ?`, id).Scan(&fonts)
	return map[string]any{"id": id, "name": name, "description": desc, "mode": mode, "everyone": everyone, "group_ids": groups, "font_count": fonts}
}

type eventUserData struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	IsAdmin     bool   `json:"is_admin"`
	Disabled    bool   `json:"disabled"`
}

func (s *Server) eventUser(id int64) *eventUserData {
	var x eventUserData
	if err := s.db.QueryRow(`SELECT id, username, display_name, email, is_admin, disabled FROM users WHERE id = ?`, id).
		Scan(&x.ID, &x.Username, &x.DisplayName, &x.Email, &x.IsAdmin, &x.Disabled); err != nil {
		return nil
	}
	return &x
}

type eventDeviceData struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	UserID   int64  `json:"user_id"`
	User     string `json:"user"`
	Platform string `json:"platform"`
	Hostname string `json:"hostname"`
}

func (s *Server) eventDevice(id int64) *eventDeviceData {
	var x eventDeviceData
	if err := s.db.QueryRow(`SELECT d.id, d.name, d.user_id, u.username, d.platform, d.hostname FROM devices d JOIN users u ON u.id = d.user_id WHERE d.id = ?`, id).
		Scan(&x.ID, &x.Name, &x.UserID, &x.User, &x.Platform, &x.Hostname); err != nil {
		return nil
	}
	return &x
}

// webhookDataDocs describes each event's data object (the license, request,
// user and device objects are the ones the API returns).
var webhookDataDocs = map[string]string{
	"font.added":           "data: {fonts: [{id, family, style, full_name, postscript_name, license_id}], by}",
	"font.deleted":         "data: {fonts: [...as font.added], by}",
	"font.license_changed": "data: {fonts: [...], license_id (null: none), by}",
	"collection.created":   "data: {collection: {id, name, description, mode, everyone, group_ids, font_count}, by}",
	"collection.updated":   "data: {collection, added_fonts?, removed_fonts?, by}",
	"collection.deleted":   "data: {collection: {id, name}, by}",
	"license.created":      "data: {license: License, by}",
	"license.updated":      "data: {license: License, changed: [field names], by}",
	"license.deleted":      "data: {license: {id, name}, by}",
	"license.expiring":     "data: {license: License, days_left}",
	"license.over_seats":   "data: {license: License, used, seats}",
	"request.created":      "data: {request: FontRequest}",
	"request.answered":     "data: {request: FontRequest}",
	"inventory.copy_found": "data: {device: {id, name, user_id, user, platform, hostname}, copies: [{family, license_id, license, copy: InventoryCopy}]}",
	"device.enrolled":      "data: {device: {id, name, user_id, user, platform, hostname}}",
	"device.removed":       "data: {device, by}",
	"user.created":         "data: {user: {id, username, display_name, email, is_admin, disabled}, by}",
	"user.updated":         "data: {user, by}",
	"user.deleted":         "data: {user: {id, username}, by}",
	"token.expiring":       "data: {token: {id, name, expires_at}, days_left}",
	"webhook.test":         "data: {message, sent_by}",
}
