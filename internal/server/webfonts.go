package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

// Web fonts: a web kit is a set of library fonts for websites. A site
// embeds one stylesheet,
//
//	<link rel="stylesheet" href="https://typehaus.example/webfonts/wk_….css">
//
// which declares the fonts (WOFF2, with WOFF as fallback). Only fonts whose
// license includes the "web" right and hasn't expired are served, only to
// the domains listed in the kit (checked against Origin and Referer), and
// every stylesheet request counts as a page view against the license's
// monthly limit, if it has one. Going over the limit doesn't break sites:
// typehaus flags the license and sends license.webfont_views.

type webkitFont struct {
	ID      int64  `json:"id"`
	Family  string `json:"family"`
	Style   string `json:"style"`
	Weight  int    `json:"weight"`
	Italic  bool   `json:"italic"`
	License string `json:"license"`
	Served  bool   `json:"served"`            // part of the stylesheet
	Problem string `json:"problem,omitempty"` // why not
}

type webkitRow struct {
	ID        int64        `json:"id"`
	Name      string       `json:"name"`
	Token     string       `json:"token"` // public: part of the stylesheet's address
	Domains   []string     `json:"domains"`
	Enabled   bool         `json:"enabled"`
	CSSURL    string       `json:"css_url"`
	Fonts     []webkitFont `json:"fonts"`
	Views     int64        `json:"views_this_month"`
	CreatedAt int64        `json:"created_at"`
	UpdatedAt int64        `json:"updated_at"`
}

type webkitReq struct {
	Name    *string   `json:"name"`
	Domains *[]string `json:"domains"` // host names; *.example.com includes subdomains; * allows every site
	FontIDs *[]int64  `json:"font_ids"`
	Enabled *bool     `json:"enabled"`
}

type webkitCreated struct {
	ID     int64  `json:"id"`
	Token  string `json:"token"`
	CSSURL string `json:"css_url"`
}

func month(t time.Time) string { return t.UTC().Format("2006-01") }

// cleanDomain turns "https://www.Example.com/page" into "www.example.com".
func cleanDomain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "" {
		return "", nil
	}
	if d == "*" {
		return d, nil
	}
	if strings.Contains(d, "://") {
		u, err := url.Parse(d)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("%q isn't a domain", d)
		}
		d = u.Host
	}
	d = strings.TrimSuffix(strings.SplitN(d, "/", 2)[0], ".")
	if h, _, err := net.SplitHostPort(d); err == nil {
		d = h
	}
	wild := strings.HasPrefix(d, "*.")
	host := strings.TrimPrefix(d, "*.")
	if host == "" || strings.ContainsAny(host, " *@?#") || (!strings.Contains(host, ".") && host != "localhost" && net.ParseIP(host) == nil) {
		return "", fmt.Errorf("%q isn't a domain", d)
	}
	if wild {
		return "*." + host, nil
	}
	return host, nil
}

// domainAllowed: host matches an entry exactly, or a *.entry subdomain.
func domainAllowed(domains []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range domains {
		switch {
		case d == "*", d == host:
			return true
		case strings.HasPrefix(d, "*.") && (host == d[2:] || strings.HasSuffix(host, d[1:])):
			return true
		}
	}
	return false
}

func newKitToken() string {
	b := make([]byte, 10)
	rand.Read(b)
	return "wk_" + hex.EncodeToString(b)
}

func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// webProblem says why a font can't be a web font ("" = it can).
func webProblem(kind, licName, rights, expiresOn string, hasLicense bool) string {
	switch {
	case kind != "sfnt":
		return "only TrueType and OpenType fonts can be web fonts"
	case !hasLicense:
		return "no license assigned – assign one that includes web use"
	case !strings.Contains(","+rights+",", ",web,"):
		return "the license “" + licName + "” doesn't include web use"
	}
	if exp, _ := expiry(expiresOn, time.Now()); exp {
		return "the license “" + licName + "” has expired"
	}
	return ""
}

type kitFontRow struct {
	webkitFont
	sha   string
	face  int
	licID sql.NullInt64
}

func (s *Server) kitFonts(kitID int64) ([]kitFontRow, error) {
	rows, err := s.db.Query(`SELECT f.id, f.family, f.style, f.weight, f.italic, f.kind, f.sha256, f.face_index, f.license_id,
		COALESCE(l.name, ''), COALESCE(l.rights, ''), COALESCE(l.expires_on, '')
		FROM webkit_fonts wf JOIN fonts f ON f.id = wf.font_id LEFT JOIN licenses l ON l.id = f.license_id
		WHERE wf.kit_id = ? ORDER BY f.family COLLATE NOCASE, f.weight, f.italic`, kitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kitFontRow
	for rows.Next() {
		var f kitFontRow
		var kind, rights, expires string
		if err := rows.Scan(&f.ID, &f.Family, &f.Style, &f.Weight, &f.Italic, &kind, &f.sha, &f.face, &f.licID,
			&f.License, &rights, &expires); err != nil {
			return nil, err
		}
		f.Problem = webProblem(kind, f.License, rights, expires, f.licID.Valid)
		f.Served = f.Problem == ""
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Server) loadKit(r *http.Request, where string, arg any) (*webkitRow, error) {
	k := &webkitRow{Fonts: []webkitFont{}, Domains: []string{}}
	var domains string
	err := s.db.QueryRow(`SELECT id, name, token, domains, enabled, created_at, updated_at FROM webkits WHERE `+where, arg).
		Scan(&k.ID, &k.Name, &k.Token, &domains, &k.Enabled, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		return nil, err
	}
	k.Domains = splitList(domains)
	if k.Domains == nil {
		k.Domains = []string{}
	}
	fonts, err := s.kitFonts(k.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range fonts {
		k.Fonts = append(k.Fonts, f.webkitFont)
	}
	s.db.QueryRow(`SELECT COALESCE(SUM(views), 0) FROM webkit_views WHERE kit_id = ? AND month = ?`, k.ID, month(time.Now())).Scan(&k.Views)
	k.Views += s.views.pending(k.ID)
	if r != nil {
		k.CSSURL = s.publicBase(r) + "/webfonts/" + k.Token + ".css"
	}
	return k, nil
}

func (s *Server) handleListWebkits(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT id FROM webkits ORDER BY name COLLATE NOCASE`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []*webkitRow{}
	for _, id := range ids {
		k, err := s.loadKit(r, "id = ?", id)
		if err != nil {
			s.internalErr(w, err)
			return
		}
		out = append(out, k)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetWebkit(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	k, err := s.loadKit(r, "id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "web kit not found")
		return
	}
	if err != nil {
		s.internalErr(w, err)
		return
	}
	writeJSON(w, 200, k)
}

func (req *webkitReq) validate() (map[string]any, []int64, error) {
	c := map[string]any{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 200 {
			return nil, nil, errors.New("name required (at most 200 characters)")
		}
		c["name"] = n
	}
	if req.Domains != nil {
		seen := map[string]bool{}
		var ds []string
		for _, d := range *req.Domains {
			cd, err := cleanDomain(d)
			if err != nil {
				return nil, nil, err
			}
			if cd != "" && !seen[cd] {
				seen[cd] = true
				ds = append(ds, cd)
			}
		}
		if len(ds) > 100 {
			return nil, nil, errors.New("at most 100 domains")
		}
		c["domains"] = strings.Join(ds, ",")
	}
	if req.Enabled != nil {
		c["enabled"] = boolInt(*req.Enabled)
	}
	var ids []int64
	if req.FontIDs != nil {
		seen := map[int64]bool{}
		for _, id := range *req.FontIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > 500 {
			return nil, nil, errors.New("at most 500 fonts per kit")
		}
	}
	return c, ids, nil
}

func (s *Server) setKitFonts(kitID int64, ids []int64) error {
	if len(ids) > 0 {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM fonts WHERE id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...).Scan(&n)
		if n != len(ids) {
			return errors.New("unknown font id")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM webkit_fonts WHERE kit_id = ?`, kitID)
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO webkit_fonts (kit_id, font_id) VALUES (?, ?)`, kitID, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) handleCreateWebkit(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req webkitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if req.Name == nil {
		writeErr(w, 400, "name required")
		return
	}
	c, ids, err := req.validate()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tok := newKitToken()
	res, err := s.db.Exec(`INSERT INTO webkits (name, token, domains, enabled, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c["name"], tok, c["domains"], 1, u.ID, now(), now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if c["domains"] == nil {
		s.db.Exec(`UPDATE webkits SET domains = '' WHERE id = ?`, id)
	}
	if req.Enabled != nil && !*req.Enabled {
		s.db.Exec(`UPDATE webkits SET enabled = 0 WHERE id = ?`, id)
	}
	if err := s.setKitFonts(id, ids); err != nil {
		s.db.Exec(`DELETE FROM webkits WHERE id = ?`, id)
		writeErr(w, 400, err.Error())
		return
	}
	s.audit(u, "webkit.create", "%s (%d fonts)", c["name"], len(ids))
	writeJSON(w, 201, webkitCreated{ID: id, Token: tok, CSSURL: s.publicBase(r) + "/webfonts/" + tok + ".css"})
}

func (s *Server) handleUpdateWebkit(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("webkits", id) {
		writeErr(w, 404, "web kit not found")
		return
	}
	var req webkitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	c, ids, err := req.validate()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c["updated_at"] = now()
	cols, args := sortedCols(c)
	var set []string
	for _, col := range cols {
		set = append(set, col+" = ?")
	}
	if _, err := s.db.Exec(`UPDATE webkits SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		s.internalErr(w, err)
		return
	}
	if req.FontIDs != nil {
		if err := s.setKitFonts(id, ids); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	s.audit(u, "webkit.update", "web kit %d", id)
	w.WriteHeader(204)
}

func (s *Server) handleDeleteWebkit(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM webkits WHERE id = ?`, id).Scan(&name); err != nil {
		writeErr(w, 404, "web kit not found")
		return
	}
	s.db.Exec(`DELETE FROM webkits WHERE id = ?`, id)
	s.audit(u, "webkit.delete", "%s", name)
	w.WriteHeader(204)
}

// ---- public delivery ----------------------------------------------------------

// requestHost is the site asking: from Origin, else Referer ("" = neither).
func requestHost(r *http.Request) string {
	for _, h := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h == "" || h == "null" {
			continue
		}
		if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}

func (s *Server) ownHost(r *http.Request, host string) bool {
	if s.cfg.BaseURL != "" {
		if u, err := url.Parse(s.cfg.BaseURL); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	h, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		h = r.Host
	}
	return strings.EqualFold(h, host)
}

func webfontErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(msg + "\n"))
}

// handleWebfontCSS serves a kit's stylesheet and counts the page view.
func (s *Server) handleWebfontCSS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !strings.HasSuffix(name, ".css") {
		http.NotFound(w, r)
		return
	}
	k, err := s.loadKit(nil, "token = ?", strings.TrimSuffix(name, ".css"))
	if err != nil || !k.Enabled {
		webfontErr(w, 404, "/* unknown or disabled web kit */")
		return
	}
	host := requestHost(r)
	allowed := host == "" || s.ownHost(r, host) || domainAllowed(k.Domains, host)
	if !allowed {
		webfontErr(w, 403, fmt.Sprintf("/* typehaus: %s isn't allowed to use this web kit – add it to the kit's domains */", host))
		return
	}
	fonts, err := s.kitFonts(k.ID)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if host != "" && !s.ownHost(r, host) {
		s.views.add(k.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "/* typehaus web kit “%s” – fonts licensed for web use */\n", strings.ReplaceAll(k.Name, "*/", ""))
	for _, f := range fonts {
		if !f.Served {
			fmt.Fprintf(&b, "/* %s %s left out: %s */\n", cssSafe(f.Family), cssSafe(f.Style), strings.ReplaceAll(f.Problem, "*/", ""))
			continue
		}
		style := "normal"
		if f.Italic {
			style = "italic"
		}
		weight := strconv.Itoa(f.Weight)
		if f.Weight <= 0 {
			weight = "400"
		}
		if lo, hi, ok := s.weightRange(f.sha, f.face); ok {
			weight = fmt.Sprintf("%d %d", lo, hi) // variable font: the whole range
		}
		file := fmt.Sprintf("%s/%d-%s", k.Token, f.ID, f.sha[:12])
		fmt.Fprintf(&b, "@font-face {\n  font-family: \"%s\";\n  font-style: %s;\n  font-weight: %s;\n  font-display: swap;\n  src: url(\"%s.woff2\") format(\"woff2\"), url(\"%s.woff\") format(\"woff\");\n}\n",
			cssSafe(f.Family), style, weight, file, file)
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	// revalidated on every page load, so each load is counted
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(b.String()))
}

func cssSafe(s string) string {
	return strings.NewReplacer(`"`, "", `\`, "", "\n", " ", "*/", "", "<", "").Replace(s)
}

var encodeMu sync.Mutex

var weightRanges sync.Map // sha-face → [3]int{lo, hi, ok}

// weightRange: the weight axis of a variable font, read once per file.
func (s *Server) weightRange(sha string, face int) (int, int, bool) {
	key := fmt.Sprintf("%s-%d", sha, face)
	if v, ok := weightRanges.Load(key); ok {
		r := v.([3]int)
		return r[0], r[1], r[2] == 1
	}
	var r [3]int
	if b, err := os.ReadFile(s.blobPath(sha)); err == nil {
		if lo, hi, ok := fontmeta.WeightRange(b, face); ok {
			r = [3]int{lo, hi, 1}
		}
	}
	weightRanges.Store(key, r)
	return r[0], r[1], r[2] == 1
}

// handleWebfontFile serves one font of a kit as WOFF2 or WOFF.
func (s *Server) handleWebfontFile(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	ext := filepath.Ext(file)
	if ext != ".woff2" && ext != ".woff" {
		http.NotFound(w, r)
		return
	}
	idPart := strings.SplitN(strings.TrimSuffix(file, ext), "-", 2)[0]
	fontID, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	k, err := s.loadKit(nil, "token = ?", r.PathValue("kit"))
	if err != nil || !k.Enabled {
		webfontErr(w, 404, "unknown or disabled web kit")
		return
	}
	host := requestHost(r)
	if host != "" && !s.ownHost(r, host) && !domainAllowed(k.Domains, host) {
		webfontErr(w, 403, host+" isn't allowed to use this web kit")
		return
	}
	fonts, err := s.kitFonts(k.ID)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	var f *kitFontRow
	for i := range fonts {
		if fonts[i].ID == fontID {
			f = &fonts[i]
		}
	}
	if f == nil || !f.Served {
		webfontErr(w, 404, "font not in this web kit, or not licensed for web use")
		return
	}
	cacheDir := filepath.Join(s.cfg.DataDir, "webfonts")
	cache := filepath.Join(cacheDir, fmt.Sprintf("%s-%d%s", f.sha, f.face, ext))
	data, err := os.ReadFile(cache)
	if err != nil {
		encodeMu.Lock()
		data, err = os.ReadFile(cache)
		if err != nil {
			var src []byte
			src, err = os.ReadFile(s.blobPath(f.sha))
			if err == nil {
				if ext == ".woff2" {
					data, err = fontmeta.EncodeWOFF2(src, f.face)
				} else {
					data, err = fontmeta.EncodeWOFF(src, f.face)
				}
			}
			if err == nil {
				os.MkdirAll(cacheDir, 0o750)
				os.WriteFile(cache, data, 0o640)
			}
		}
		encodeMu.Unlock()
		if err != nil {
			s.log.Warn("web font encoding failed", "font", f.ID, "err", err)
			webfontErr(w, 500, "the font couldn't be converted")
			return
		}
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Set("Vary", "Origin")
	}
	ct := "font/woff2"
	if ext == ".woff" {
		ct = "font/woff"
	}
	w.Header().Set("Content-Type", ct)
	// the address changes with the file (it holds the file's hash)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// ---- page views ----------------------------------------------------------------

// viewCounter batches page views in memory; flushed every few seconds.
type viewCounter struct {
	mu sync.Mutex
	n  map[int64]int64
}

func (v *viewCounter) add(kit int64) {
	v.mu.Lock()
	if v.n == nil {
		v.n = map[int64]int64{}
	}
	v.n[kit]++
	v.mu.Unlock()
}

func (v *viewCounter) pending(kit int64) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.n[kit]
}

func (v *viewCounter) take() map[int64]int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := v.n
	v.n = nil
	return n
}

func (s *Server) flushViews() {
	m := month(time.Now())
	for kit, n := range s.views.take() {
		s.db.Exec(`INSERT INTO webkit_views (kit_id, month, views) VALUES (?, ?, ?)
			ON CONFLICT(kit_id, month) DO UPDATE SET views = views + excluded.views`, kit, m, n)
	}
}

// licenseWebViews: this month's page views of the kits using each license.
func (s *Server) licenseWebViews() map[int64]int64 {
	out := map[int64]int64{}
	rows, err := s.db.Query(`SELECT k.license_id, SUM(v.views) FROM webkit_views v
		JOIN (SELECT DISTINCT wf.kit_id, f.license_id FROM webkit_fonts wf JOIN fonts f ON f.id = wf.font_id WHERE f.license_id IS NOT NULL) k
		ON k.kit_id = v.kit_id WHERE v.month = ? GROUP BY k.license_id`, month(time.Now()))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		rows.Scan(&id, &n)
		out[id] = n
	}
	return out
}
