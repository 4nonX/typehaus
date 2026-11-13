package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/protocol"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---- devices (user-facing) ----------------------------------------------------

type deviceRow struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Hostname     string `json:"hostname"`
	AgentVersion string `json:"agent_version"`
	LastSeen     *int64 `json:"last_seen"`
	LastIP       string `json:"last_ip"`
	CreatedAt    int64  `json:"created_at"`
	Installed    int    `json:"installed"`
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request, u *authUser) {
	q := `SELECT d.id, d.user_id, u.username, d.name, d.platform, d.hostname, d.agent_version, d.last_seen, d.last_ip, d.created_at,
		(SELECT COUNT(*) FROM device_fonts df WHERE df.device_id = d.id)
		FROM devices d JOIN users u ON u.id = d.user_id`
	var args []any
	if !(u.IsAdmin && r.URL.Query().Get("all") == "1") {
		q += ` WHERE d.user_id = ?`
		args = append(args, u.ID)
	}
	q += ` ORDER BY u.username, d.name`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []deviceRow{}
	for rows.Next() {
		var d deviceRow
		var ls sql.NullInt64
		rows.Scan(&d.ID, &d.UserID, &d.Username, &d.Name, &d.Platform, &d.Hostname, &d.AgentVersion, &ls, &d.LastIP, &d.CreatedAt, &d.Installed)
		if ls.Valid {
			d.LastSeen = &ls.Int64
		}
		out = append(out, d)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var res sql.Result
	var err error
	if u.IsAdmin {
		res, err = s.db.Exec(`DELETE FROM devices WHERE id = ?`, id)
	} else {
		res, err = s.db.Exec(`DELETE FROM devices WHERE id = ? AND user_id = ?`, id, u.ID)
	}
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "device not found")
		return
	}
	s.audit(u, "device.revoke", "device %d", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCreateEnrollCode(w http.ResponseWriter, r *http.Request, u *authUser) {
	// human-friendly code: XXXX-XXXX-XXXX (base32, no ambiguous padding)
	raw := strings.ToUpper(randomToken(8))[:12]
	code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:]
	exp := time.Now().Add(15 * time.Minute)
	if _, err := s.db.Exec(`INSERT INTO enroll_codes (code_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(normalizeCode(code)), u.ID, exp.Unix()); err != nil {
		s.internalErr(w, err)
		return
	}
	server := s.cfg.BaseURL
	if server == "" {
		scheme := "http"
		if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
			scheme = "https"
		}
		server = scheme + "://" + r.Host
	}
	writeJSON(w, 201, map[string]any{"code": code, "expires_at": exp.Unix(), "server": server})
}

func normalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
}

// ---- agent endpoints ------------------------------------------------------------

func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnrollRequest
	if !readJSON(w, r, &req) {
		return
	}
	key := "enroll|" + s.clientIP(r)
	if !s.limiter.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	ch := hashToken(normalizeCode(req.Code))
	var userID int64
	var username string
	err := s.db.QueryRow(`SELECT e.user_id, u.username FROM enroll_codes e JOIN users u ON u.id = e.user_id
		WHERE e.code_hash = ? AND e.expires_at > ? AND u.disabled = 0`, ch, now()).Scan(&userID, &username)
	if err != nil {
		s.limiter.fail(key)
		writeErr(w, http.StatusUnauthorized, "invalid or expired enrollment code")
		return
	}
	s.db.Exec(`DELETE FROM enroll_codes WHERE code_hash = ?`, ch)

	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		name = req.Hostname
	}
	if name == "" {
		name = "device"
	}
	tok := "thd_" + randomToken(32)
	res, err := s.db.Exec(`INSERT INTO devices (user_id, name, platform, hostname, agent_version, token_hash, last_seen, last_ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, trunc(name, 100), trunc(req.Platform, 20), trunc(req.Hostname, 100), trunc(req.AgentVersion, 40),
		hashToken(tok), now(), s.clientIP(r), now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(&authUser{ID: userID, Username: username}, "device.enroll", "%s (%s, %s)", name, req.Platform, req.Hostname)
	writeJSON(w, 201, protocol.EnrollResponse{DeviceID: id, Token: tok, User: username})
}

// agentSupportsKind reports whether an agent version can install a kind of
// font. Agents before 0.3 know TrueType/OpenType and install other single
// files (bitmap fonts) correctly too, but not Type 1 with metrics files.
func agentSupportsKind(agentVersion, kind string) bool {
	if kind != "type1" {
		return true
	}
	v, ok := parseSemver(agentVersion)
	if !ok {
		return true // dev builds
	}
	return v[0] > 0 || v[1] >= 3
}

func parseSemver(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if _, err := fmt.Sscan(p, &v[i]); err != nil {
			return v, false
		}
	}
	return v, true
}

// manifestFonts returns the distinct font files a user should have active,
// limited to formats the device's operating system (and agent) can install.
// d may be nil (all platforms).
func (s *Server) manifestFonts(userID int64, d *authDevice) ([]protocol.ManifestFont, error) {
	var isAdmin bool
	if err := s.db.QueryRow(`SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&isAdmin); err != nil {
		return nil, err
	}
	where, vargs := visibleClause(&authUser{ID: userID, IsAdmin: isAdmin})
	args := append([]any{userID}, vargs...)
	// newest first, so the duplicate filter below keeps the newest upload
	rows, err := s.db.Query(`SELECT f.id, f.sha256, f.filename, f.family, f.style, f.full_name, f.format, f.size, f.postscript_name,
		f.kind, f.platforms, f.extras, l.id, COALESCE(l.enforce, 0), COALESCE(l.seats, 0), COALESCE(l.seat_unit, ''), COALESCE(l.expires_on, ''), COALESCE(l.lease_days, 0)
		FROM fonts f JOIN user_fonts a ON a.font_id = f.id AND a.user_id = ?
		LEFT JOIN licenses l ON l.id = f.license_id
		WHERE `+where+` ORDER BY f.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ManifestFont{}
	seen := map[string]bool{}
	psSeen := map[string]bool{}
	var gate *deviceLicenseGate
	if d != nil {
		gate = &deviceLicenseGate{s: s, device: d.ID, machine: d.Machine, user: d.UserID, nowT: time.Now(),
			holders: map[int64]map[int64]bool{}, people: map[int64]map[int64]bool{}}
	}
	type row struct {
		m                     protocol.ManifestFont
		ps, platforms, extras string
		lid                   sql.NullInt64
		enforce               bool
		seats                 int
		unit, expires         string
	}
	var all []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.m.ID, &x.m.SHA256, &x.m.Filename, &x.m.Family, &x.m.Style, &x.m.FullName, &x.m.Format, &x.m.Size, &x.ps,
			&x.m.Kind, &x.platforms, &x.extras, &x.lid, &x.enforce, &x.seats, &x.unit, &x.expires, &x.m.LeaseDays); err != nil {
			return nil, err
		}
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close() // the license gate queries the database: single connection
	for _, x := range all {
		m, ps, platforms, extras := x.m, x.ps, x.platforms, x.extras
		if d != nil {
			if !gate.allow(x.lid, x.enforce, x.seats, x.unit, x.expires) {
				continue // license expired or no free device seat
			}
			if platforms != "" && d.Platform != "" && !strings.Contains(","+platforms+",", ","+d.Platform+",") {
				continue // e.g. a Windows bitmap font for a Mac
			}
			if !agentSupportsKind(d.Agent, m.Kind) {
				continue
			}
		}
		if extras != "" && extras != "[]" {
			json.Unmarshal([]byte(extras), &m.Extras)
		}
		if m.Kind == "sfnt" {
			m.Kind = "" // keep the manifest identical for TrueType/OpenType (older agents)
		}
		// Safety net: never ship two files with the same PostScript name.
		if ps != "" {
			if psSeen[ps] && !seen[m.SHA256] {
				continue
			}
			psSeen[ps] = true
		}
		if seen[m.SHA256] { // TTC: one file, several faces
			continue
		}
		seen[m.SHA256] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 }) // stable ETag
	return out, nil
}

func (s *Server) handleAgentManifest(w http.ResponseWriter, r *http.Request, d *authDevice) {
	fonts, err := s.manifestFonts(d.UserID, d)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%d|%s|%t|", protocol.Version, d.User, s.cfg.PollSeconds, Version, s.cfg.Inventory)
	for _, f := range fonts {
		fmt.Fprintf(h, "%s%s%s%d", f.SHA256, f.Filename, f.Kind, f.LeaseDays)
		for _, e := range f.Extras {
			h.Write([]byte(e.SHA256 + e.Ext))
		}
	}
	etag := `"` + hex.EncodeToString(h.Sum(nil))[:32] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, 200, protocol.Manifest{
		ProtocolVersion: protocol.Version,
		User:            d.User,
		PollSeconds:     s.cfg.PollSeconds,
		ServerVersion:   Version,
		Fonts:           fonts,
		Inventory:       s.cfg.Inventory,
	})
}

func (s *Server) handleAgentFont(w http.ResponseWriter, r *http.Request, d *authDevice) {
	sha := r.PathValue("sha")
	if !shaRe.MatchString(sha) {
		writeErr(w, 400, "bad hash")
		return
	}
	fonts, err := s.manifestFonts(d.UserID, d)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	allowed := false
	for _, f := range fonts {
		if f.SHA256 == sha {
			allowed = true
		}
		for _, e := range f.Extras {
			if e.SHA256 == sha {
				allowed = true
			}
		}
	}
	if !allowed {
		writeErr(w, 404, "font not active for this user")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, s.blobPath(sha))
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request, d *authDevice) {
	var rep protocol.Report
	if !readJSON(w, r, &rep) {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.internalErr(w, err)
		return
	}
	tx.Exec(`UPDATE devices SET agent_version = ? WHERE id = ?`, trunc(rep.AgentVersion, 40), d.ID)
	keep := map[string]bool{}
	for _, sha := range rep.Installed {
		if shaRe.MatchString(sha) {
			keep[sha] = true
			tx.Exec(`INSERT OR IGNORE INTO device_fonts (device_id, sha256, installed_at) VALUES (?, ?, ?)`, d.ID, sha, now())
		}
	}
	rows, err := tx.Query(`SELECT sha256 FROM device_fonts WHERE device_id = ?`, d.ID)
	var stale []string
	if err == nil {
		for rows.Next() {
			var sha string
			rows.Scan(&sha)
			if !keep[sha] {
				stale = append(stale, sha)
			}
		}
		rows.Close()
	}
	for _, sha := range stale {
		tx.Exec(`DELETE FROM device_fonts WHERE device_id = ? AND sha256 = ?`, d.ID, sha)
	}
	if err := tx.Commit(); err != nil {
		s.internalErr(w, err)
		return
	}
	for _, e := range rep.Errors {
		s.log.Warn("agent reported error", "device", d.ID, "user", d.User, "sha256", e.SHA256, "error", trunc(e.Message, 300))
	}
	s.usageChanged()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
