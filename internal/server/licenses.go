package server

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// License management: what was bought (or which open license applies), what
// it allows, how many seats, for how long, the documents that prove it, and
// – optionally – enforcement of seats and term.

// usage rights a license can grant
var licenseRights = []string{"desktop", "web", "app", "ebook", "video", "logo"}

const dateLayout = "2006-01-02"

type licenseDoc struct {
	ID        int64  `json:"id"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	Shared    bool   `json:"shared"`
	CreatedAt int64  `json:"created_at"`
}

type licenseRow struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Vendor      string       `json:"vendor"`
	Kind        string       `json:"kind"` // commercial | open
	SPDX        string       `json:"spdx,omitempty"`
	Rights      []string     `json:"rights"`
	Seats       int          `json:"seats"`     // 0 = unlimited
	SeatUnit    string       `json:"seat_unit"` // users | devices
	Enforce     bool         `json:"enforce"`
	Download    bool         `json:"downloadable"`
	Reference   string       `json:"reference,omitempty"` // admins only
	URL         string       `json:"url"`
	PurchasedOn string       `json:"purchased_on"`
	ExpiresOn   string       `json:"expires_on"`
	Notes       string       `json:"notes,omitempty"` // admins only
	CreatedAt   int64        `json:"created_at"`
	Families    int          `json:"families"`
	Fonts       int          `json:"fonts"`
	Users       int          `json:"users"`
	Devices     int          `json:"devices"`
	Used        int          `json:"used"` // users or devices, per seat unit
	Holder      bool         `json:"holder"`
	Expired     bool         `json:"expired"`
	ExpiresIn   *int         `json:"expires_in_days,omitempty"`
	Over        bool         `json:"over"`
	Full        bool         `json:"full"`
	Documents   []licenseDoc `json:"documents"`
}

// status is a one-word summary for reports.
func (l *licenseRow) status() string {
	switch {
	case l.Expired:
		return "expired"
	case l.Over:
		return "over seats"
	case l.ExpiresIn != nil && *l.ExpiresIn <= 30:
		return "expires soon"
	case l.Full:
		return "all seats in use"
	}
	return "ok"
}

// expiry reports whether a term end date ("" = perpetual) has passed and
// how many days are left.
func expiry(expiresOn string, at time.Time) (expired bool, daysLeft *int) {
	if expiresOn == "" {
		return false, nil
	}
	end, err := time.ParseInLocation(dateLayout, expiresOn, time.Local)
	if err != nil {
		return false, nil
	}
	end = end.AddDate(0, 0, 1) // valid through the whole end date
	if !at.Before(end) {
		return true, nil
	}
	d := int(end.Sub(at).Hours() / 24)
	return false, &d
}

const licenseSelect = `SELECT l.id, l.name, l.vendor, l.kind, l.spdx, l.rights, l.seats, l.seat_unit, l.enforce, l.downloadable,
	l.reference, l.url, l.purchased_on, l.expires_on, l.notes, l.created_at,
	(SELECT COUNT(DISTINCT f.family) FROM fonts f WHERE f.license_id = l.id),
	(SELECT COUNT(*) FROM fonts f WHERE f.license_id = l.id),
	(SELECT COUNT(DISTINCT a.user_id) FROM activations a JOIN fonts f ON f.id = a.font_id WHERE f.license_id = l.id),
	(SELECT COUNT(DISTINCT df.device_id) FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256 WHERE f.license_id = l.id),
	EXISTS(SELECT 1 FROM activations a JOIN fonts f ON f.id = a.font_id WHERE f.license_id = l.id AND a.user_id = ?)
	FROM licenses l`

func (s *Server) queryLicenses(u *authUser, where string, args ...any) ([]*licenseRow, error) {
	q := licenseSelect
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := s.db.Query(q+` ORDER BY l.name COLLATE NOCASE`, append([]any{u.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	out := []*licenseRow{}
	byID := map[int64]*licenseRow{}
	nowT := time.Now()
	for rows.Next() {
		l := &licenseRow{Documents: []licenseDoc{}}
		var rights string
		if err := rows.Scan(&l.ID, &l.Name, &l.Vendor, &l.Kind, &l.SPDX, &rights, &l.Seats, &l.SeatUnit, &l.Enforce, &l.Download,
			&l.Reference, &l.URL, &l.PurchasedOn, &l.ExpiresOn, &l.Notes, &l.CreatedAt,
			&l.Families, &l.Fonts, &l.Users, &l.Devices, &l.Holder); err != nil {
			rows.Close()
			return nil, err
		}
		l.Rights = splitList(rights)
		l.Used = l.Users
		if l.SeatUnit == "devices" {
			l.Used = l.Devices
		}
		l.Expired, l.ExpiresIn = expiry(l.ExpiresOn, nowT)
		l.Over = l.Seats > 0 && l.Used > l.Seats
		l.Full = l.Seats > 0 && l.Used >= l.Seats
		if !u.IsAdmin {
			l.Reference, l.Notes = "", ""
		}
		out = append(out, l)
		byID[l.ID] = l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dq := `SELECT id, license_id, filename, size, shared, created_at FROM license_documents`
	if !u.IsAdmin {
		dq += ` WHERE shared = 1`
	}
	drows, err := s.db.Query(dq + ` ORDER BY filename COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var d licenseDoc
		var lid int64
		drows.Scan(&d.ID, &lid, &d.Filename, &d.Size, &d.Shared, &d.CreatedAt)
		if l := byID[lid]; l != nil {
			l.Documents = append(l.Documents, d)
		}
	}
	return out, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func splitList(s string) []string {
	out := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// licensesVisibleTo limits non-admins to licenses of fonts they can see.
func licensesVisibleTo(u *authUser) (string, []any) {
	if u.IsAdmin {
		return "", nil
	}
	where, args := visibleClause(u)
	return `l.id IN (SELECT f.license_id FROM fonts f WHERE f.license_id IS NOT NULL AND ` + where + `)`, args
}

func (s *Server) handleListLicenses(w http.ResponseWriter, r *http.Request, u *authUser) {
	where, args := licensesVisibleTo(u)
	out, err := s.queryLicenses(u, where, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// handleGetLicense returns one license with its families and who holds a seat.
func (s *Server) handleGetLicense(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	where, args := licensesVisibleTo(u)
	if where != "" {
		where += " AND "
	}
	ls, err := s.queryLicenses(u, where+"l.id = ?", append(args, id)...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if len(ls) == 0 {
		writeErr(w, 404, "license not found")
		return
	}
	type famRow struct {
		Family string `json:"family"`
		Styles int    `json:"styles"`
	}
	type holder struct {
		Name  string `json:"name"`
		Extra string `json:"extra,omitempty"`
		Since int64  `json:"since"`
	}
	fams := []famRow{}
	fw, fargs := visibleClause(u)
	rows, err := s.db.Query(`SELECT f.family, COUNT(*) FROM fonts f WHERE f.license_id = ? AND `+fw+
		` GROUP BY f.family ORDER BY f.family COLLATE NOCASE`, append([]any{id}, fargs...)...)
	if err == nil {
		for rows.Next() {
			var f famRow
			rows.Scan(&f.Family, &f.Styles)
			fams = append(fams, f)
		}
		rows.Close()
	}
	resp := map[string]any{"license": ls[0], "families": fams}
	if u.IsAdmin {
		holders := []holder{}
		var hq string
		if ls[0].SeatUnit == "devices" {
			hq = `SELECT d.name, u.username, MIN(df.installed_at) FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256
				JOIN devices d ON d.id = df.device_id JOIN users u ON u.id = d.user_id
				WHERE f.license_id = ? GROUP BY d.id ORDER BY d.name COLLATE NOCASE`
		} else {
			hq = `SELECT COALESCE(NULLIF(u.display_name, ''), u.username), u.username, MIN(a.created_at) FROM activations a
				JOIN fonts f ON f.id = a.font_id JOIN users u ON u.id = a.user_id
				WHERE f.license_id = ? GROUP BY u.id ORDER BY 1 COLLATE NOCASE`
		}
		if rows, err := s.db.Query(hq, id); err == nil {
			for rows.Next() {
				var h holder
				rows.Scan(&h.Name, &h.Extra, &h.Since)
				if h.Extra == h.Name {
					h.Extra = ""
				}
				holders = append(holders, h)
			}
			rows.Close()
		}
		resp["holders"] = holders
	}
	writeJSON(w, 200, resp)
}

type licenseReq struct {
	Name        *string   `json:"name"`
	Vendor      *string   `json:"vendor"`
	Kind        *string   `json:"kind"`
	Rights      *[]string `json:"rights"`
	Seats       *int      `json:"seats"`
	SeatUnit    *string   `json:"seat_unit"`
	Enforce     *bool     `json:"enforce"`
	Download    *bool     `json:"downloadable"`
	Reference   *string   `json:"reference"`
	URL         *string   `json:"url"`
	PurchasedOn *string   `json:"purchased_on"`
	ExpiresOn   *string   `json:"expires_on"`
	Notes       *string   `json:"notes"`
}

// columns validates the request and returns the columns to set.
func (req *licenseReq) columns() (map[string]any, error) {
	c := map[string]any{}
	str := func(col string, p *string, max int) {
		if p != nil {
			v := strings.TrimSpace(*p)
			if len(v) > max {
				v = v[:max]
			}
			c[col] = v
		}
	}
	str("name", req.Name, 200)
	str("vendor", req.Vendor, 200)
	str("reference", req.Reference, 200)
	str("notes", req.Notes, 5000)
	if v, ok := c["name"]; ok && v == "" {
		return nil, errors.New("name required")
	}
	if req.Kind != nil {
		if *req.Kind != "commercial" && *req.Kind != "open" {
			return nil, errors.New("kind must be commercial or open")
		}
		c["kind"] = *req.Kind
	}
	if req.SeatUnit != nil {
		if *req.SeatUnit != "users" && *req.SeatUnit != "devices" {
			return nil, errors.New("seat_unit must be users or devices")
		}
		c["seat_unit"] = *req.SeatUnit
	}
	if req.Seats != nil {
		c["seats"] = max(*req.Seats, 0)
	}
	if req.Enforce != nil {
		c["enforce"] = boolInt(*req.Enforce)
	}
	if req.Download != nil {
		c["downloadable"] = boolInt(*req.Download)
	}
	if req.Rights != nil {
		var keep []string
		for _, known := range licenseRights {
			for _, r := range *req.Rights {
				if r == known {
					keep = append(keep, r)
					break
				}
			}
		}
		c["rights"] = strings.Join(keep, ",")
	}
	if req.URL != nil {
		v := strings.TrimSpace(*req.URL)
		if v != "" {
			pu, err := url.Parse(v)
			if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
				return nil, errors.New("terms URL must start with https:// or http://")
			}
		}
		c["url"] = v
	}
	for col, p := range map[string]*string{"purchased_on": req.PurchasedOn, "expires_on": req.ExpiresOn} {
		if p == nil {
			continue
		}
		v := strings.TrimSpace(*p)
		if v != "" {
			if _, err := time.Parse(dateLayout, v); err != nil {
				return nil, fmt.Errorf("%s must be a date (YYYY-MM-DD)", strings.ReplaceAll(col, "_", " "))
			}
		}
		c[col] = v
	}
	return c, nil
}

func (s *Server) handleCreateLicense(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req licenseReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeErr(w, 400, "name required")
		return
	}
	cols, err := req.columns()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, ok := cols["rights"]; !ok {
		cols["rights"] = "desktop"
	}
	cols["created_at"] = now()
	names, vals := sortedCols(cols)
	res, err := s.db.Exec(`INSERT INTO licenses (`+strings.Join(names, ", ")+`) VALUES (`+placeholders(len(names))+`)`, vals...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(u, "license.create", "%s", cols["name"])
	writeJSON(w, 201, map[string]int64{"id": id})
}

func sortedCols(c map[string]any) ([]string, []any) {
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	sort.Strings(names)
	vals := make([]any, len(names))
	for i, k := range names {
		vals[i] = c[k]
	}
	return names, vals
}

func (s *Server) handleUpdateLicense(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req licenseReq
	if !readJSON(w, r, &req) {
		return
	}
	cols, err := req.columns()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(cols) == 0 {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	names, vals := sortedCols(cols)
	for i := range names {
		names[i] += " = ?"
	}
	res, err := s.db.Exec(`UPDATE licenses SET `+strings.Join(names, ", ")+` WHERE id = ?`, append(vals, id)...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "license not found")
		return
	}
	s.audit(u, "license.update", "license %d: %s", id, strings.Join(sortedKeys(cols), ", "))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func sortedKeys(c map[string]any) []string {
	k, _ := sortedCols(c)
	return k
}

func (s *Server) handleDeleteLicense(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var name string
	s.db.QueryRow(`SELECT name FROM licenses WHERE id = ?`, id).Scan(&name)
	var shas []string
	if rows, err := s.db.Query(`SELECT sha256 FROM license_documents WHERE license_id = ?`, id); err == nil {
		for rows.Next() {
			var sha string
			rows.Scan(&sha)
			shas = append(shas, sha)
		}
		rows.Close()
	}
	if _, err := s.db.Exec(`DELETE FROM licenses WHERE id = ?`, id); err != nil {
		s.internalErr(w, err)
		return
	}
	for _, sha := range shas {
		s.removeBlobIfUnused(sha)
	}
	s.audit(u, "license.delete", "%s", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// removeBlobIfUnused deletes a stored file once no font, companion file or
// license document refers to it any more.
func (s *Server) removeBlobIfUnused(sha string) {
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM fonts WHERE sha256 = ?1 OR instr(extras, ?1) > 0)
		+ (SELECT COUNT(*) FROM license_documents WHERE sha256 = ?1)`, sha).Scan(&n)
	if n == 0 {
		if err := os.Remove(s.blobPath(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("remove blob", "sha", sha, "err", err)
		}
	}
}

// ---- documents ------------------------------------------------------------------

const maxLicenseDoc = 50 << 20

func (s *Server) handleUploadLicenseDocs(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var exists bool
	s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM licenses WHERE id = ?)`, id).Scan(&exists)
	if !exists {
		writeErr(w, 404, "license not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadMB<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a multipart upload")
		return
	}
	shared := true
	var added []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, "upload interrupted or too large")
			return
		}
		if p.FormName() == "shared" {
			v, _ := io.ReadAll(io.LimitReader(p, 8))
			shared = string(v) != "0" && string(v) != "false"
			continue
		}
		if p.FileName() == "" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(p, maxLicenseDoc+1))
		if err != nil {
			writeErr(w, 400, "upload interrupted or too large")
			return
		}
		name := docFilename(p.FileName())
		if len(data) > maxLicenseDoc {
			writeErr(w, 413, name+" is larger than 50 MB")
			return
		}
		if len(data) == 0 {
			continue
		}
		sha, err := s.storeFile(data)
		if err != nil {
			s.internalErr(w, err)
			return
		}
		if _, err := s.db.Exec(`INSERT INTO license_documents (license_id, sha256, filename, size, shared, uploaded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sha, name, len(data), boolInt(shared), u.ID, now()); err != nil {
			s.internalErr(w, err)
			return
		}
		added = append(added, name)
	}
	if len(added) == 0 {
		writeErr(w, 400, "no file received")
		return
	}
	s.audit(u, "license.document.add", "license %d: %s", id, strings.Join(added, ", "))
	writeJSON(w, 201, map[string]any{"added": added})
}

// docFilename keeps a readable base name (any script) but drops paths and
// control characters.
func docFilename(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, `\`, "/"))
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}
		return r
	}, n)
	n = strings.TrimSpace(n)
	if n == "" || n == "." || n == "/" {
		n = "document"
	}
	if len(n) > 200 {
		ext := filepath.Ext(n)
		if len(ext) > 20 {
			ext = ""
		}
		n = n[:200-len(ext)] + ext
	}
	return n
}

func docID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("doc"), 10, 64)
	return id, err == nil
}

func (s *Server) handleLicenseDoc(w http.ResponseWriter, r *http.Request, u *authUser) {
	lid, ok1 := pathID(r)
	did, ok2 := docID(r)
	if !ok1 || !ok2 {
		writeErr(w, 400, "bad id")
		return
	}
	var sha, name string
	var shared bool
	if err := s.db.QueryRow(`SELECT sha256, filename, shared FROM license_documents WHERE id = ? AND license_id = ?`, did, lid).
		Scan(&sha, &name, &shared); err != nil {
		writeErr(w, 404, "document not found")
		return
	}
	if !u.IsAdmin {
		where, args := licensesVisibleTo(u)
		var visible bool
		s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM licenses l WHERE l.id = ? AND `+where+`)`, append([]any{lid}, args...)...).Scan(&visible)
		if !shared || !visible {
			writeErr(w, 404, "document not found")
			return
		}
	}
	// Only formats that cannot run script are shown in the browser; anything
	// else (HTML, SVG, office files…) is downloaded.
	inline := map[string]string{".pdf": "application/pdf", ".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}
	ext := strings.ToLower(filepath.Ext(name))
	disp := "attachment"
	ct := "application/octet-stream"
	if t, ok := inline[ext]; ok && r.URL.Query().Get("download") != "1" {
		disp, ct = "inline", t
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, no-cache")
	f, err := os.Open(s.blobPath(sha))
	if err != nil {
		writeErr(w, 404, "document file missing")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) handleUpdateLicenseDoc(w http.ResponseWriter, r *http.Request, u *authUser) {
	lid, ok1 := pathID(r)
	did, ok2 := docID(r)
	if !ok1 || !ok2 {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Shared   *bool   `json:"shared"`
		Filename *string `json:"filename"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Shared != nil {
		s.db.Exec(`UPDATE license_documents SET shared = ? WHERE id = ? AND license_id = ?`, boolInt(*req.Shared), did, lid)
	}
	if req.Filename != nil {
		s.db.Exec(`UPDATE license_documents SET filename = ? WHERE id = ? AND license_id = ?`, docFilename(*req.Filename), did, lid)
	}
	s.audit(u, "license.document.update", "license %d, document %d", lid, did)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteLicenseDoc(w http.ResponseWriter, r *http.Request, u *authUser) {
	lid, ok1 := pathID(r)
	did, ok2 := docID(r)
	if !ok1 || !ok2 {
		writeErr(w, 400, "bad id")
		return
	}
	var sha, name string
	if err := s.db.QueryRow(`SELECT sha256, filename FROM license_documents WHERE id = ? AND license_id = ?`, did, lid).Scan(&sha, &name); err != nil {
		writeErr(w, 404, "document not found")
		return
	}
	s.db.Exec(`DELETE FROM license_documents WHERE id = ?`, did)
	s.removeBlobIfUnused(sha)
	s.audit(u, "license.document.delete", "license %d: %s", lid, name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- open-source license recognition --------------------------------------------

type openLicense struct {
	SPDX, Name, URL string
	match           []string // lower-case needles in the font's license description/URL
}

// Order matters: more specific first (AGPL before GPL).
var openLicenses = []openLicense{
	{"OFL-1.1", "SIL Open Font License 1.1", "https://openfontlicense.org", []string{"open font license", "scripts.sil.org/ofl", "openfontlicense.org", "sil.org/ofl"}},
	{"Apache-2.0", "Apache License 2.0", "https://www.apache.org/licenses/LICENSE-2.0", []string{"apache.org/licenses", "apache license"}},
	{"UFL-1.0", "Ubuntu Font Licence 1.0", "https://ubuntu.com/legal/font-licence", []string{"ubuntu font licence", "ubuntu font license", "font-licence"}},
	{"AGPL-3.0", "GNU Affero GPL 3.0", "https://www.gnu.org/licenses/agpl-3.0.html", []string{"affero"}},
	{"GPL-FE", "GNU GPL with font exception", "https://www.gnu.org/licenses/gpl-faq.html#FontException", []string{"gnu general public license", "gnu.org/licenses/gpl", "gnu gpl"}},
	{"LPPL-1.3c", "LaTeX Project Public License", "https://www.latex-project.org/lppl/", []string{"latex project public license", "latex-project.org/lppl"}},
	{"GUST", "GUST Font License", "https://www.gust.org.pl/projects/e-foundry/licenses", []string{"gust font license", "gust.org.pl"}},
	{"Bitstream-Vera", "Bitstream Vera Fonts License", "https://www.gnome.org/fonts/", []string{"bitstream vera"}},
	{"MIT", "MIT License", "https://opensource.org/license/mit", []string{"opensource.org/licenses/mit", "mit license"}},
	{"CC0-1.0", "CC0 1.0 (public domain)", "https://creativecommons.org/publicdomain/zero/1.0/", []string{"publicdomain/zero", "cc0"}},
}

func detectOpenLicense(text, link string) *openLicense {
	hay := strings.ToLower(text + "\n" + link)
	if strings.TrimSpace(hay) == "" {
		return nil
	}
	for i := range openLicenses {
		for _, n := range openLicenses[i].match {
			if strings.Contains(hay, n) {
				return &openLicenses[i]
			}
		}
	}
	return nil
}

// autoLicense assigns recognised open-source licenses to fonts that have no
// license yet; ids nil means the whole library. It returns how many fonts
// were assigned and the names of licenses it had to create.
func (s *Server) autoLicense(ids []int64) (int, []string, error) {
	q := `SELECT id, license_text, license_url FROM fonts WHERE license_id IS NULL`
	var args []any
	if ids != nil {
		if len(ids) == 0 {
			return 0, nil, nil
		}
		q += ` AND id IN (` + placeholders(len(ids)) + `)`
		args = int64Args(ids)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return 0, nil, err
	}
	bySPDX := map[string][]int64{}
	for rows.Next() {
		var id int64
		var text, link string
		rows.Scan(&id, &text, &link)
		if ol := detectOpenLicense(text, link); ol != nil {
			bySPDX[ol.SPDX] = append(bySPDX[ol.SPDX], id)
		}
	}
	rows.Close()
	var created []string
	n := 0
	for _, ol := range openLicenses {
		fids := bySPDX[ol.SPDX]
		if len(fids) == 0 {
			continue
		}
		var lid int64
		err := s.db.QueryRow(`SELECT id FROM licenses WHERE spdx = ? ORDER BY id LIMIT 1`, ol.SPDX).Scan(&lid)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := s.db.Exec(`INSERT INTO licenses (name, kind, spdx, rights, url, notes, created_at)
				VALUES (?, 'open', ?, ?, ?, 'Recognised automatically from the fonts'' embedded license information.', ?)`,
				ol.Name, ol.SPDX, strings.Join(licenseRights, ","), ol.URL, now())
			if err != nil {
				return n, created, err
			}
			lid, _ = res.LastInsertId()
			created = append(created, ol.Name)
		} else if err != nil {
			return n, created, err
		}
		res, err := s.db.Exec(`UPDATE fonts SET license_id = ? WHERE license_id IS NULL AND id IN (`+placeholders(len(fids))+`)`,
			append([]any{lid}, int64Args(fids)...)...)
		if err != nil {
			return n, created, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, created, nil
}

func (s *Server) handleDetectLicenses(w http.ResponseWriter, r *http.Request, u *authUser) {
	n, created, err := s.autoLicense(nil)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if n > 0 {
		s.audit(u, "license.detect", "%d font(s) assigned", n)
	}
	if created == nil {
		created = []string{}
	}
	writeJSON(w, 200, map[string]any{"assigned": n, "created": created})
}

// ---- enforcement ----------------------------------------------------------------

// activationRefusals checks enforced licenses before a user activates fonts.
// It returns the fonts that must not be activated and why.
func (s *Server) activationRefusals(userID int64, fontIDs []int64) (map[int64]bool, []string) {
	if len(fontIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT f.id, l.id, l.name, l.seats, l.seat_unit, l.expires_on,
		EXISTS(SELECT 1 FROM activations a WHERE a.font_id = f.id AND a.user_id = ?)
		FROM fonts f JOIN licenses l ON l.id = f.license_id
		WHERE l.enforce = 1 AND f.id IN (`+placeholders(len(fontIDs))+`)`, append([]any{userID}, int64Args(fontIDs)...)...)
	if err != nil {
		s.log.Error("license check", "err", err)
		return nil, nil
	}
	type lic struct {
		name, unit, expires string
		seats               int
		fonts               []int64
	}
	lics := map[int64]*lic{}
	var order []int64
	for rows.Next() {
		var fid, lid int64
		var l lic
		var already bool
		rows.Scan(&fid, &lid, &l.name, &l.seats, &l.unit, &l.expires, &already)
		if already {
			continue
		}
		if lics[lid] == nil {
			lics[lid] = &l
			order = append(order, lid)
		}
		lics[lid].fonts = append(lics[lid].fonts, fid)
	}
	rows.Close()
	blocked := map[int64]bool{}
	var why []string
	nowT := time.Now()
	for _, lid := range order {
		l := lics[lid]
		reason := ""
		if expired, _ := expiry(l.expires, nowT); expired {
			reason = fmt.Sprintf("the license “%s” expired on %s", l.name, l.expires)
		} else if l.unit == "users" && l.seats > 0 {
			var holder bool
			var used int
			s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM activations a JOIN fonts f ON f.id = a.font_id WHERE f.license_id = ?1 AND a.user_id = ?2),
				(SELECT COUNT(DISTINCT a.user_id) FROM activations a JOIN fonts f ON f.id = a.font_id WHERE f.license_id = ?1)`,
				lid, userID).Scan(&holder, &used)
			if !holder && used >= l.seats {
				reason = fmt.Sprintf("all %d seats of the license “%s” are in use", l.seats, l.name)
			}
		}
		if reason != "" {
			for _, f := range l.fonts {
				blocked[f] = true
			}
			why = append(why, reason)
		}
	}
	return blocked, why
}

// deviceLicenseGate decides, while building a device's manifest, whether a
// font under an enforced license may be installed there: not after the term
// ended, and for per-device licenses only on devices that already hold a
// seat or while seats are free.
type deviceLicenseGate struct {
	s       *Server
	device  int64
	nowT    time.Time
	holders map[int64]map[int64]bool
}

func (g *deviceLicenseGate) allow(lid sql.NullInt64, enforce bool, seats int, unit, expires string) bool {
	if !lid.Valid || !enforce {
		return true
	}
	if expired, _ := expiry(expires, g.nowT); expired {
		return false
	}
	if unit != "devices" || seats <= 0 {
		return true
	}
	h := g.holders[lid.Int64]
	if h == nil {
		h = map[int64]bool{}
		rows, err := g.s.db.Query(`SELECT DISTINCT df.device_id FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256
			WHERE f.license_id = ?`, lid.Int64)
		if err == nil {
			for rows.Next() {
				var d int64
				rows.Scan(&d)
				h[d] = true
			}
			rows.Close()
		}
		g.holders[lid.Int64] = h
	}
	if h[g.device] {
		return true
	}
	if len(h) >= seats {
		return false
	}
	h[g.device] = true // takes the seat
	return true
}

// ---- report -----------------------------------------------------------------------

func (s *Server) handleLicenseReport(w http.ResponseWriter, r *http.Request, u *authUser) {
	ls, err := s.queryLicenses(u, "")
	if err != nil {
		s.internalErr(w, err)
		return
	}
	holders := map[int64][]string{}
	if rows, err := s.db.Query(`SELECT DISTINCT f.license_id, u.username FROM activations a JOIN fonts f ON f.id = a.font_id
		JOIN users u ON u.id = a.user_id WHERE f.license_id IS NOT NULL ORDER BY u.username COLLATE NOCASE`); err == nil {
		for rows.Next() {
			var lid int64
			var name string
			rows.Scan(&lid, &name)
			holders[lid] = append(holders[lid], name)
		}
		rows.Close()
	}
	var unassigned int
	s.db.QueryRow(`SELECT COUNT(DISTINCT family) FROM fonts WHERE license_id IS NULL`).Scan(&unassigned)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": "typehaus-licenses-" + time.Now().Format(dateLayout) + ".csv"}))
	w.Write([]byte("\ufeff")) // Excel: UTF-8
	cw := csv.NewWriter(w)
	cw.Write([]string{"License", "Type", "Vendor", "Usage rights", "Seats", "Seat unit", "In use", "Status", "Enforced",
		"Direct download", "Purchased", "Expires", "Reference", "Families", "Styles", "Users with active fonts", "Devices", "Terms URL", "Documents"})
	for _, l := range ls {
		seats := "unlimited"
		if l.Seats > 0 {
			seats = strconv.Itoa(l.Seats)
		}
		docs := make([]string, len(l.Documents))
		for i, d := range l.Documents {
			docs[i] = d.Filename
		}
		enforced := "no"
		if l.Enforce {
			enforced = "yes"
		}
		cw.Write([]string{l.Name, l.Kind, l.Vendor, strings.Join(l.Rights, ", "), seats, l.SeatUnit, strconv.Itoa(l.Used),
			l.status(), enforced, yesNo(l.Download), l.PurchasedOn, l.ExpiresOn, l.Reference, strconv.Itoa(l.Families), strconv.Itoa(l.Fonts),
			strings.Join(holders[l.ID], ", "), strconv.Itoa(l.Devices), l.URL, strings.Join(docs, "; ")})
	}
	if unassigned > 0 {
		cw.Write([]string{"(no license assigned)", "", "", "", "", "", "", fmt.Sprintf("%d families", unassigned)})
	}
	cw.Flush()
	s.audit(u, "license.export", "%d licenses", len(ls))
}
