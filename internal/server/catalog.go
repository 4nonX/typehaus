package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The free font catalog: families from Google Fonts, Font Squirrel and
// Fontsource that an admin can search and import into the library.
//
//   - Google Fonts families come complete from Google's own repository
//     (github.com/google/fonts), with their license.
//   - Font Squirrel families come as the desktop download, with the license
//     text Font Squirrel ships.
//   - Other Fontsource families are listed but can't be imported: Fontsource
//     only publishes web subsets of them.
//
// The index comes from the Fontsource API (which also lists the Google Fonts
// families with their license) and the Font Squirrel API, and is refreshed
// once a day.

// Upstream addresses; tests point them at a fake.
var (
	fontsourceAPI   = "https://api.fontsource.org/v1/fonts"
	fontsourceCDN   = "https://cdn.jsdelivr.net/fontsource/fonts"
	fontSquirrelURL = "https://www.fontsquirrel.com"
	googleFontsRaw  = "https://raw.githubusercontent.com/google/fonts/main"
)

const catalogMaxAge = 24 * time.Hour

// catalogEntry is one family in the index (also its on-disk cache form).
type catalogEntry struct {
	ID       string `json:"id"`
	Family   string `json:"family"`
	Source   string `json:"source"`
	Category string `json:"category"`
	Styles   int    `json:"styles"`
	Variable bool   `json:"variable"`
	License  string `json:"license"`
	Foundry  string `json:"foundry,omitempty"`
	Page     string `json:"page"`
	Reason   string `json:"reason,omitempty"`
	// for previews and imports
	Ref    string `json:"ref"` // Fontsource id or Font Squirrel url name
	Subset string `json:"subset,omitempty"`
	Weight int    `json:"weight,omitempty"`
	Italic bool   `json:"italic_only,omitempty"`
}

// catalogFamily is a search result.
type catalogFamily struct {
	ID         string `json:"id"`
	Family     string `json:"family"`
	Source     string `json:"source"`   // google, fontsquirrel, fontsource
	Category   string `json:"category"` // sans-serif, serif, display, handwriting, monospace
	Styles     int    `json:"styles"`   // number of styles (weights × italics), 0 if unknown
	Variable   bool   `json:"variable"`
	License    string `json:"license"` // SPDX id, or "Freeware" for Font Squirrel
	Foundry    string `json:"foundry,omitempty"`
	Page       string `json:"page"`
	Importable bool   `json:"importable"`
	Reason     string `json:"reason,omitempty"` // why it can't be imported
	InLibrary  bool   `json:"in_library"`
}

type catalogCache struct {
	mu      sync.Mutex
	entries []*catalogEntry
	byID    map[string]*catalogEntry
	loaded  time.Time
	tried   time.Time
}

// familyKey compares family names across sources: "Open Sans" = "opensans".
func familyKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) catalogFile() string { return filepath.Join(s.cfg.DataDir, "catalog", "index.json") }

// catalogIndex returns the index, loading or refreshing it when needed.
func (s *Server) catalogIndex(ctx context.Context) ([]*catalogEntry, error) {
	c := s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		if b, err := os.ReadFile(s.catalogFile()); err == nil {
			var disk struct {
				Loaded  int64           `json:"loaded"`
				Entries []*catalogEntry `json:"entries"`
			}
			if json.Unmarshal(b, &disk) == nil && len(disk.Entries) > 0 {
				c.set(disk.Entries, time.Unix(disk.Loaded, 0))
			}
		}
	}
	fresh := time.Since(c.loaded) < catalogMaxAge
	if fresh || (c.entries != nil && time.Since(c.tried) < 10*time.Minute) {
		return c.entries, nil
	}
	c.tried = time.Now()
	entries, err := s.fetchCatalog(ctx)
	if err != nil {
		if c.entries != nil {
			s.log.Warn("catalog refresh failed, using the cached index", "err", err)
			return c.entries, nil
		}
		return nil, err
	}
	c.set(entries, time.Now())
	if b, err := json.Marshal(map[string]any{"loaded": c.loaded.Unix(), "entries": entries}); err == nil {
		os.MkdirAll(filepath.Dir(s.catalogFile()), 0o750)
		os.WriteFile(s.catalogFile(), b, 0o640)
	}
	return c.entries, nil
}

func (c *catalogCache) set(e []*catalogEntry, at time.Time) {
	c.entries = e
	c.loaded = at
	c.byID = make(map[string]*catalogEntry, len(e))
	for _, x := range e {
		c.byID[x.ID] = x
	}
}

func (s *Server) catalogEntry(ctx context.Context, id string) (*catalogEntry, error) {
	if _, err := s.catalogIndex(ctx); err != nil {
		return nil, err
	}
	s.catalog.mu.Lock()
	defer s.catalog.mu.Unlock()
	return s.catalog.byID[id], nil
}

// get fetches a URL. Font Squirrel's firewall answers bursts with an empty
// 202, so that is retried a few times after a pause.
func (s *Server) get(ctx context.Context, u string) (*http.Response, error) {
	for try := 0; ; try++ {
		if err := paceSquirrel(ctx, u); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.outbound.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 200 {
			return resp, nil
		}
		resp.Body.Close()
		if resp.StatusCode != 202 && resp.StatusCode != 429 && resp.StatusCode != 503 || try == 3 {
			return nil, fmt.Errorf("%s: HTTP %d", hostOf(u), resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second << try):
		}
	}
}

// Font Squirrel throttles quick successive requests from one address, so
// typehaus spaces its requests there.
var squirrelPace struct {
	sync.Mutex
	next time.Time
}

const squirrelGap = 400 * time.Millisecond

func paceSquirrel(ctx context.Context, u string) error {
	if !strings.HasPrefix(u, fontSquirrelURL) {
		return nil
	}
	squirrelPace.Lock()
	wait := time.Until(squirrelPace.next)
	if wait < 0 {
		wait = 0
	}
	squirrelPace.next = time.Now().Add(wait + squirrelGap)
	squirrelPace.Unlock()
	if wait == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

func (s *Server) getJSON(ctx context.Context, u string, v any) error {
	resp, err := s.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return u
}

type fontsourceFont struct {
	ID        string   `json:"id"`
	Family    string   `json:"family"`
	Subsets   []string `json:"subsets"`
	Weights   []int    `json:"weights"`
	Styles    []string `json:"styles"`
	DefSubset string   `json:"defSubset"`
	Variable  any      `json:"variable"` // false or an axes object
	Category  string   `json:"category"`
	License   string   `json:"license"`
	Type      string   `json:"type"`
}

type fontSquirrelFamily struct {
	Name           string `json:"family_name"`
	URLName        string `json:"family_urlname"`
	Foundry        string `json:"foundry_name"`
	Classification string `json:"classification"`
	Count          string `json:"family_count"`
}

func (s *Server) fetchCatalog(ctx context.Context) ([]*catalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var fs []fontsourceFont
	var sq []fontSquirrelFamily
	var fsErr, sqErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); fsErr = s.getJSON(ctx, fontsourceAPI, &fs) }()
	go func() { defer wg.Done(); sqErr = s.getJSON(ctx, fontSquirrelURL+"/api/fontlist/all", &sq) }()
	wg.Wait()
	if fsErr != nil && sqErr != nil {
		return nil, fmt.Errorf("font catalogs unreachable: %v; %v", fsErr, sqErr)
	}
	if fsErr != nil {
		s.log.Warn("Fontsource catalog unavailable", "err", fsErr)
	}
	if sqErr != nil {
		s.log.Warn("Font Squirrel catalog unavailable", "err", sqErr)
	}
	seen := map[string]bool{}
	var out []*catalogEntry
	add := func(e *catalogEntry) {
		k := familyKey(e.Family)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, e)
	}
	// Google Fonts first: complete files and a known license.
	for _, typ := range []string{"google", "other"} {
		for _, f := range fs {
			if f.Type != typ || f.ID == "" || f.Family == "" {
				continue
			}
			e := &catalogEntry{Family: f.Family, Category: f.Category, License: f.License, Ref: f.ID,
				Subset: f.DefSubset, Variable: f.Variable != nil && f.Variable != false}
			e.Styles = len(f.Weights) * max(len(f.Styles), 1)
			e.Weight = nearestWeight(f.Weights, 400)
			e.Italic = len(f.Styles) == 1 && f.Styles[0] == "italic"
			if typ == "google" {
				e.ID, e.Source = "google:"+f.ID, "google"
				e.Page = "https://fonts.google.com/specimen/" + url.PathEscape(f.Family)
			} else {
				e.ID, e.Source = "fontsource:"+f.ID, "fontsource"
				e.Page = "https://fontsource.org/fonts/" + f.ID
				e.Reason = "Fontsource only publishes web subsets of this family; get the complete fonts from its designer."
			}
			add(e)
		}
	}
	for _, f := range sq {
		if f.URLName == "" || f.Name == "" {
			continue
		}
		n := 0
		fmt.Sscan(f.Count, &n)
		add(&catalogEntry{ID: "fontsquirrel:" + f.URLName, Family: f.Name, Source: "fontsquirrel",
			Category: squirrelCategory(f.Classification), Styles: n, License: "Freeware",
			Foundry: f.Foundry, Page: fontSquirrelURL + "/fonts/" + f.URLName, Ref: f.URLName})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Family) < strings.ToLower(out[j].Family) })
	return out, nil
}

func nearestWeight(ws []int, want int) int {
	best := 0
	for _, w := range ws {
		if best == 0 || abs(w-want) < abs(best-want) {
			best = w
		}
	}
	if best == 0 {
		return want
	}
	return best
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

func squirrelCategory(c string) string {
	c = strings.ToLower(c)
	switch {
	case strings.Contains(c, "sans"):
		return "sans-serif"
	case strings.Contains(c, "serif"):
		return "serif"
	case strings.Contains(c, "mono"), strings.Contains(c, "typewriter"), strings.Contains(c, "programming"):
		return "monospace"
	case strings.Contains(c, "script"), strings.Contains(c, "hand"), strings.Contains(c, "calligraph"):
		return "handwriting"
	}
	return "display"
}

// libraryFamilies returns the family keys and catalog refs already in the
// library.
func (s *Server) libraryFamilies() (map[string]bool, map[string]bool) {
	keys, refs := map[string]bool{}, map[string]bool{}
	rows, err := s.db.Query(`SELECT DISTINCT family, family_auto, catalog_ref FROM fonts`)
	if err != nil {
		return keys, refs
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, ref string
		rows.Scan(&a, &b, &ref)
		keys[familyKey(a)] = true
		keys[familyKey(b)] = true
		if ref != "" {
			refs[ref] = true
		}
	}
	return keys, refs
}

func (e *catalogEntry) result(keys, refs map[string]bool) catalogFamily {
	return catalogFamily{ID: e.ID, Family: e.Family, Source: e.Source, Category: e.Category, Styles: e.Styles,
		Variable: e.Variable, License: e.License, Foundry: e.Foundry, Page: e.Page, Importable: e.Reason == "",
		Reason: e.Reason, InLibrary: refs[e.ID] || keys[familyKey(e.Family)]}
}

func (s *Server) catalogOff(w http.ResponseWriter) bool {
	if !s.cfg.Catalog {
		writeErrCode(w, 404, "feature_disabled", "the free font catalog is switched off on this server (TYPEHAUS_CATALOG)")
		return true
	}
	return false
}

func (s *Server) handleCatalogSearch(w http.ResponseWriter, r *http.Request, u *authUser) {
	if s.catalogOff(w) {
		return
	}
	entries, err := s.catalogIndex(r.Context())
	if err != nil {
		writeErrCode(w, 502, "upstream_error", err.Error())
		return
	}
	q := r.URL.Query()
	words := strings.Fields(strings.ToLower(q.Get("q")))
	cat, src, lic := q.Get("category"), q.Get("source"), q.Get("license")
	keys, refs := s.libraryFamilies()
	qk := familyKey(q.Get("q"))
	type hit struct {
		f    catalogFamily
		rank int
	}
	var hits []hit
	for _, e := range entries {
		if cat != "" && e.Category != cat || src != "" && e.Source != src || lic != "" && !strings.EqualFold(e.License, lic) {
			continue
		}
		name := strings.ToLower(e.Family + " " + e.Foundry)
		ok := true
		for _, wd := range words {
			if !strings.Contains(name, wd) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		// exact name, then names starting with the query; then Google Fonts
		// (complete, well-known), Font Squirrel, and what can't be imported.
		rank := 3
		fk := familyKey(e.Family)
		switch {
		case qk != "" && fk == qk:
			rank = 0
		case qk != "" && strings.HasPrefix(fk, qk):
			rank = 1
		case e.Reason != "":
			rank = 4
		case e.Source == "google":
			rank = 2
		}
		hits = append(hits, hit{e.result(keys, refs), rank})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]catalogFamily, len(hits))
	for i, h := range hits {
		out[i] = h.f
	}
	writeJSON(w, 200, out)
}

// handleCatalogPreview serves a sample of a catalog family: a WOFF2 font for
// Google Fonts and Fontsource, an image for Font Squirrel. Fetched once, then
// kept, so browsers only ever talk to typehaus.
func (s *Server) handleCatalogPreview(w http.ResponseWriter, r *http.Request, u *authUser) {
	if s.catalogOff(w) {
		return
	}
	e, err := s.catalogEntry(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErrCode(w, 502, "upstream_error", err.Error())
		return
	}
	if e == nil {
		writeErr(w, 404, "not in the catalog")
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "catalog", "preview")
	name := strings.NewReplacer(":", "_", "/", "_", "\\", "_", "..", "_").Replace(e.ID)
	for _, ext := range []string{".woff2", ".png"} {
		if b, err := os.ReadFile(filepath.Join(dir, name+ext)); err == nil {
			servePreview(w, ext, b)
			return
		}
	}
	// a page of results asks for many previews at once; fetch a few at a time
	select {
	case previewSlots <- struct{}{}:
		defer func() { <-previewSlots }()
	case <-r.Context().Done():
		return
	}
	var src, ext string
	switch e.Source {
	case "google", "fontsource":
		style := "normal"
		if e.Italic {
			style = "italic"
		}
		subset := e.Subset
		if subset == "" {
			subset = "latin"
		}
		src, ext = fmt.Sprintf("%s/%s@latest/%s-%d-%s.woff2", fontsourceCDN, url.PathEscape(e.Ref), subset, e.Weight, style), ".woff2"
	case "fontsquirrel":
		var info []struct {
			Image string `json:"listing_image"`
		}
		if err := s.getJSON(r.Context(), fontSquirrelURL+"/api/familyinfo/"+url.PathEscape(e.Ref), &info); err != nil || len(info) == 0 {
			msg := "no preview available"
			if err != nil {
				msg += ": " + err.Error()
			}
			writeErrCode(w, 502, "upstream_error", msg)
			return
		}
		src, ext = info[0].Image, ".png"
		pu, err := url.Parse(src)
		trusted := err == nil && (strings.HasPrefix(src, fontSquirrelURL+"/") ||
			pu.Scheme == "https" && (pu.Hostname() == "fontsquirrel.com" || strings.HasSuffix(pu.Hostname(), ".fontsquirrel.com")))
		if !trusted {
			writeErrCode(w, 502, "upstream_error", "no preview available")
			return
		}
	}
	b, err := s.fetchBytes(r.Context(), src, 4<<20)
	if err != nil {
		writeErrCode(w, 502, "upstream_error", err.Error())
		return
	}
	os.MkdirAll(dir, 0o750)
	os.WriteFile(filepath.Join(dir, name+ext), b, 0o640)
	servePreview(w, ext, b)
}

func servePreview(w http.ResponseWriter, ext string, b []byte) {
	if ext == ".png" {
		w.Header().Set("Content-Type", "image/png")
	} else {
		w.Header().Set("Content-Type", "font/woff2")
	}
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(b)
}

var errTooLarge = errors.New("file too large")

var previewSlots = make(chan struct{}, 3)

func (s *Server) fetchBytes(ctx context.Context, u string, limit int64) ([]byte, error) {
	resp, err := s.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// ---- import ----------------------------------------------------------------

type catalogImportReq struct {
	IDs          []string `json:"ids"`
	CollectionID int64    `json:"collection_id,omitempty"`
}

type catalogImportResult struct {
	ID       string   `json:"id"`
	Family   string   `json:"family"`
	Status   string   `json:"status"`   // added, exists, error
	Added    int      `json:"added"`    // styles added
	FontIDs  []int64  `json:"font_ids"` // the family's fonts in the library, new or not
	Licenses []string `json:"licenses"` // licenses the fonts were assigned
	Message  string   `json:"message,omitempty"`
}

func (s *Server) handleCatalogImport(w http.ResponseWriter, r *http.Request, u *authUser) {
	if s.catalogOff(w) {
		return
	}
	var req catalogImportReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 50 {
		writeErr(w, 400, "ids: give 1 to 50 catalog families")
		return
	}
	if req.CollectionID > 0 && !s.exists("collections", req.CollectionID) {
		writeErr(w, 404, "collection not found")
		return
	}
	if _, err := s.catalogIndex(r.Context()); err != nil {
		writeErrCode(w, 502, "upstream_error", err.Error())
		return
	}
	var results []catalogImportResult
	var all []int64
	for _, id := range req.IDs {
		res := s.importCatalogFamily(r.Context(), id, u)
		results = append(results, res)
		all = append(all, res.FontIDs...)
	}
	if req.CollectionID > 0 {
		for _, id := range all {
			s.db.Exec(`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id) VALUES (?, ?)`, req.CollectionID, id)
		}
	}
	if len(all) > 0 {
		s.emit("font.added", map[string]any{"fonts": s.eventFonts(all), "by": u.Username})
	}
	writeJSON(w, 200, results)
}

func (s *Server) importCatalogFamily(ctx context.Context, id string, u *authUser) catalogImportResult {
	res := catalogImportResult{ID: id, Status: "error", FontIDs: []int64{}, Licenses: []string{}}
	s.catalog.mu.Lock()
	e := s.catalog.byID[id]
	s.catalog.mu.Unlock()
	if e == nil {
		res.Message = "not in the catalog"
		return res
	}
	res.Family = e.Family
	if e.Reason != "" {
		res.Message = e.Reason
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var files []upFile
	var licText, licSPDX string
	var err error
	switch e.Source {
	case "google":
		files, licText, licSPDX, err = s.fetchGoogleFamily(ctx, e)
	case "fontsquirrel":
		files, licText, err = s.fetchSquirrelFamily(ctx, e)
	default:
		err = errors.New("this family can't be imported")
	}
	if err != nil {
		res.Message = err.Error()
		s.log.Warn("catalog import failed", "family", e.Family, "err", err)
		return res
	}
	results, ids := s.ingestBatch(files, u, false)
	res.FontIDs = append(res.FontIDs, ids...)
	var errMsg string
	for _, x := range results {
		switch x.Status {
		case "added":
			res.Added += max(len(x.Faces), 1)
		case "error":
			if errMsg == "" {
				errMsg = x.Message
			}
		}
	}
	if res.Added == 0 {
		if len(ids) > 0 {
			res.Status, res.Message = "exists", "already in the library"
		} else {
			res.Message = errMsg
			if res.Message == "" {
				res.Message = "no usable font files"
			}
		}
		return res
	}
	res.Status = "added"
	s.db.Exec(`UPDATE fonts SET catalog_ref = ? WHERE id IN (`+placeholders(len(ids))+`)`, append([]any{e.ID}, int64Args(ids)...)...)
	if _, _, err := s.autoLicense(ids); err != nil {
		s.log.Warn("catalog import: license detection", "err", err)
	}
	if err := s.licenseCatalogFonts(e, ids, licText, licSPDX, files, u); err != nil {
		s.log.Warn("catalog import: license", "err", err)
	}
	rows, err := s.db.Query(`SELECT DISTINCT l.name FROM fonts f JOIN licenses l ON l.id = f.license_id WHERE f.id IN (`+placeholders(len(ids))+`) ORDER BY 1`, int64Args(ids)...)
	if err == nil {
		for rows.Next() {
			var n string
			rows.Scan(&n)
			res.Licenses = append(res.Licenses, n)
		}
		rows.Close()
	}
	s.audit(u, "catalog.import", "%s from %s: %d style(s)", e.Family, e.Source, len(ids))
	return res
}

// licenseCatalogFonts assigns a license to imported fonts the embedded
// license text didn't settle: the license Google lists, an open license
// recognised in the shipped license text, or a new license record holding
// the Font Squirrel license text for the admin to review.
func (s *Server) licenseCatalogFonts(e *catalogEntry, ids []int64, licText, licSPDX string, files []upFile, u *authUser) error {
	var open []int64
	rows, err := s.db.Query(`SELECT id FROM fonts WHERE license_id IS NULL AND id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		open = append(open, id)
	}
	rows.Close()
	if len(open) == 0 {
		return nil
	}
	ol := openLicenseBySPDX(licSPDX)
	if ol == nil {
		ol = detectOpenLicense(licText, "")
	}
	var lid int64
	if ol != nil {
		if lid, _, err = s.openLicenseID(ol); err != nil {
			return err
		}
	} else {
		name := e.Family + " – Font Squirrel freeware"
		res, err := s.db.Exec(`INSERT INTO licenses (name, vendor, kind, rights, url, notes, created_at) VALUES (?, ?, 'commercial', '', ?, ?, ?)`,
			name, e.Foundry, e.Page, "Imported from Font Squirrel, which lists this family as free for commercial use. Read the license document for what it allows, then tick the rights.", now())
		if err != nil {
			return err
		}
		lid, _ = res.LastInsertId()
		for _, f := range files {
			if !isLicenseDoc(f.name) {
				continue
			}
			sha, err := s.storeFile(f.data)
			if err != nil {
				return err
			}
			s.db.Exec(`INSERT INTO license_documents (license_id, sha256, filename, kind, size, shared, uploaded_by, created_at)
				VALUES (?, ?, ?, 'eula', ?, 1, ?, ?)`, lid, sha, docFilename(f.name), len(f.data), u.ID, now())
		}
	}
	_, err = s.db.Exec(`UPDATE fonts SET license_id = ? WHERE license_id IS NULL AND id IN (`+placeholders(len(open))+`)`,
		append([]any{lid}, int64Args(open)...)...)
	return err
}

func isLicenseDoc(name string) bool {
	n := strings.ToLower(filepath.Base(name))
	ext := filepath.Ext(n)
	return (ext == ".txt" || ext == ".pdf" || ext == ".md" || ext == ".html") &&
		(strings.Contains(n, "licen") || strings.Contains(n, "eula") || strings.Contains(n, "ofl") || strings.Contains(n, "readme") || strings.Contains(n, "terms"))
}

var (
	metaFilename = regexp.MustCompile(`(?m)^\s*filename:\s*"([^"]+)"`)
	metaLicense  = regexp.MustCompile(`(?m)^license:\s*"([^"]+)"`)
)

// fetchGoogleFamily downloads a family's complete fonts from Google's
// repository: ofl/, apache/ or ufl/<family>/ with METADATA.pb naming the files.
func (s *Server) fetchGoogleFamily(ctx context.Context, e *catalogEntry) ([]upFile, string, string, error) {
	dir := familyKey(e.Family)
	var meta []byte
	var lic string
	for _, l := range []string{"ofl", "apache", "ufl"} {
		b, err := s.fetchBytes(ctx, googleFontsRaw+"/"+l+"/"+dir+"/METADATA.pb", 1<<20)
		if err == nil {
			meta, lic = b, l
			break
		}
		if ctx.Err() != nil {
			return nil, "", "", ctx.Err()
		}
	}
	if meta == nil {
		return nil, "", "", fmt.Errorf("%s isn't in the Google Fonts repository (or it couldn't be reached)", e.Family)
	}
	spdx := map[string]string{"OFL": "OFL-1.1", "APACHE2": "Apache-2.0", "UFL": "UFL-1.0"}[firstMatch(metaLicense, meta)]
	licName := map[string]string{"ofl": "OFL.txt", "apache": "LICENSE.txt", "ufl": "UFL.txt"}[lic]
	base := googleFontsRaw + "/" + lic + "/" + dir + "/"
	var files []upFile
	total := int64(0)
	limit := s.cfg.MaxUploadMB << 20
	seen := map[string]bool{}
	for _, m := range metaFilename.FindAllSubmatch(meta, -1) {
		fn := string(m[1])
		if seen[fn] || strings.Contains(fn, "/") || strings.Contains(fn, "..") {
			continue
		}
		seen[fn] = true
		b, err := s.fetchBytes(ctx, base+url.PathEscape(fn), maxFontFile)
		if err != nil {
			return nil, "", "", fmt.Errorf("%s: %w", fn, err)
		}
		total += int64(len(b))
		if total > limit {
			return nil, "", "", fmt.Errorf("the family is larger than the upload limit (%d MB)", s.cfg.MaxUploadMB)
		}
		files = append(files, upFile{name: fn, data: b})
	}
	if len(files) == 0 {
		return nil, "", "", errors.New("the repository lists no font files for this family")
	}
	licText := ""
	if b, err := s.fetchBytes(ctx, base+licName, 1<<20); err == nil {
		licText = string(b)
	}
	return files, licText, spdx, nil
}

func firstMatch(re *regexp.Regexp, b []byte) string {
	if m := re.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// fetchSquirrelFamily downloads Font Squirrel's desktop package of a family.
func (s *Server) fetchSquirrelFamily(ctx context.Context, e *catalogEntry) ([]upFile, string, error) {
	resp, err := s.get(ctx, fontSquirrelURL+"/fonts/download/"+url.PathEscape(e.Ref))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	files, errs := s.readZip(e.Ref+".zip", io.LimitReader(resp.Body, s.cfg.MaxUploadMB<<20))
	if len(files) == 0 {
		if len(errs) > 0 {
			return nil, "", errors.New("Font Squirrel: " + errs[0].Message)
		}
		return nil, "", errors.New("Font Squirrel sent no files")
	}
	var lic strings.Builder
	for _, f := range files {
		if isLicenseDoc(f.name) && len(f.data) < 1<<20 {
			lic.Write(f.data)
			lic.WriteString("\n")
		}
	}
	return files, lic.String(), nil
}
