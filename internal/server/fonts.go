package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

const maxFontFile = 64 << 20 // single font file cap

// visibleClause restricts fonts (alias f) to what a user may see. Admins see all.
func visibleClause(u *authUser) (string, []any) {
	if u.IsAdmin {
		return "1=1", nil
	}
	return `f.id IN (SELECT cf.font_id FROM collection_fonts cf JOIN collections c ON c.id = cf.collection_id
		WHERE c.everyone = 1 OR c.id IN (SELECT cg.collection_id FROM collection_groups cg
			JOIN user_groups ug ON ug.group_id = cg.group_id WHERE ug.user_id = ?))`, []any{u.ID}
}

type fontRow struct {
	ID             int64    `json:"id"`
	SHA256         string   `json:"sha256"`
	FaceIndex      int      `json:"face_index"`
	Filename       string   `json:"filename"`
	Format         string   `json:"format"`
	Size           int64    `json:"size"`
	Family         string   `json:"family"`
	Style          string   `json:"style"`
	FullName       string   `json:"full_name"`
	PostScriptName string   `json:"postscript_name"`
	Version        string   `json:"version"`
	Designer       string   `json:"designer"`
	Manufacturer   string   `json:"manufacturer"`
	Weight         int      `json:"weight"`
	Italic         bool     `json:"italic"`
	FSType         int      `json:"fs_type"`
	LicenseID      *int64   `json:"license_id"`
	Activated      bool     `json:"activated"`     // switched on by the user
	Required       bool     `json:"required"`      // installed by a collection; can't be switched off
	RequiredBy     []string `json:"required_by"`   // those collections
	Conflict       bool     `json:"conflict"`      // another file in the library has the same PostScript name
	Kind           string   `json:"kind"`          // sfnt, type1, winfnt, pcf, bdf
	Platforms      string   `json:"platforms"`     // "" = all, else comma list
	SourceFormat   string   `json:"source_format"` // original format when converted
	Collections    []int64  `json:"collections"`
	CreatedAt      int64    `json:"created_at"`
}

const fontCols = `f.id, f.sha256, f.face_index, f.filename, f.format, f.size, f.family, f.style, f.full_name,
	f.postscript_name, f.version, f.designer, f.manufacturer, f.weight, f.italic, f.fs_type, f.license_id, f.created_at`

func scanFont(sc interface{ Scan(...any) error }, f *fontRow) error {
	var lic sql.NullInt64
	err := sc.Scan(&f.ID, &f.SHA256, &f.FaceIndex, &f.Filename, &f.Format, &f.Size, &f.Family, &f.Style, &f.FullName,
		&f.PostScriptName, &f.Version, &f.Designer, &f.Manufacturer, &f.Weight, &f.Italic, &f.FSType, &lic, &f.CreatedAt)
	if lic.Valid {
		f.LicenseID = &lic.Int64
	}
	return err
}

func (s *Server) handleListFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	where, args := visibleClause(u)
	q := `SELECT ` + fontCols + `, EXISTS(SELECT 1 FROM activations a WHERE a.font_id = f.id AND a.user_id = ?),
		f.postscript_name != '' AND EXISTS(SELECT 1 FROM fonts f2 WHERE f2.postscript_name = f.postscript_name AND f2.sha256 != f.sha256),
		f.kind, f.platforms, f.source_format
		FROM fonts f WHERE ` + where
	args = append([]any{u.ID}, args...)
	if search := strings.TrimSpace(r.URL.Query().Get("q")); search != "" {
		q += ` AND (f.family LIKE ? OR f.full_name LIKE ? OR f.postscript_name LIKE ? OR f.designer LIKE ? OR f.manufacturer LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like, like, like, like)
	}
	if c, err := strconv.ParseInt(r.URL.Query().Get("collection"), 10, 64); err == nil {
		q += ` AND f.id IN (SELECT font_id FROM collection_fonts WHERE collection_id = ?)`
		args = append(args, c)
	}
	if r.URL.Query().Get("activated") == "1" {
		q += ` AND f.id IN (SELECT font_id FROM user_fonts WHERE user_id = ?)`
		args = append(args, u.ID)
	}
	q += ` ORDER BY f.family COLLATE NOCASE, f.italic, f.weight, f.style`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	out := []*fontRow{}
	byID := map[int64]*fontRow{}
	for rows.Next() {
		f := &fontRow{Collections: []int64{}, RequiredBy: []string{}}
		var lic sql.NullInt64
		if err := rows.Scan(&f.ID, &f.SHA256, &f.FaceIndex, &f.Filename, &f.Format, &f.Size, &f.Family, &f.Style, &f.FullName,
			&f.PostScriptName, &f.Version, &f.Designer, &f.Manufacturer, &f.Weight, &f.Italic, &f.FSType, &lic, &f.CreatedAt,
			&f.Activated, &f.Conflict, &f.Kind, &f.Platforms, &f.SourceFormat); err != nil {
			rows.Close()
			s.internalErr(w, err)
			return
		}
		if lic.Valid {
			f.LicenseID = &lic.Int64
		}
		out = append(out, f)
		byID[f.ID] = f
	}
	rows.Close()
	if rr, err := s.db.Query(`SELECT rf.font_id, c.name FROM required_fonts rf JOIN collections c ON c.id = rf.collection_id
		WHERE rf.user_id = ? ORDER BY c.name COLLATE NOCASE`, u.ID); err == nil {
		for rr.Next() {
			var fid int64
			var name string
			rr.Scan(&fid, &name)
			if f := byID[fid]; f != nil {
				f.Required = true
				f.RequiredBy = append(f.RequiredBy, name)
			}
		}
		rr.Close()
	}
	cr, err := s.db.Query(`SELECT collection_id, font_id FROM collection_fonts`)
	if err == nil {
		for cr.Next() {
			var cid, fid int64
			cr.Scan(&cid, &fid)
			if f := byID[fid]; f != nil {
				f.Collections = append(f.Collections, cid)
			}
		}
		cr.Close()
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetFont(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	where, args := visibleClause(u)
	var f fontRow
	var copyright, licText, licURL string
	row := s.db.QueryRow(`SELECT `+fontCols+`, f.copyright, f.license_text, f.license_url FROM fonts f WHERE f.id = ? AND `+where,
		append([]any{id}, args...)...)
	var lic sql.NullInt64
	err := row.Scan(&f.ID, &f.SHA256, &f.FaceIndex, &f.Filename, &f.Format, &f.Size, &f.Family, &f.Style, &f.FullName,
		&f.PostScriptName, &f.Version, &f.Designer, &f.Manufacturer, &f.Weight, &f.Italic, &f.FSType, &lic, &f.CreatedAt,
		&copyright, &licText, &licURL)
	if err != nil {
		writeErr(w, 404, "font not found")
		return
	}
	if lic.Valid {
		f.LicenseID = &lic.Int64
	}
	writeJSON(w, 200, map[string]any{"font": f, "copyright": copyright, "license_text": licText, "license_url": licURL})
}

// handleFontFile serves the raw font for browser previews.
func (s *Server) handleFontFile(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	where, args := visibleClause(u)
	var sha, name, format string
	err := s.db.QueryRow(`SELECT f.sha256, f.filename, f.format FROM fonts f WHERE f.id = ? AND `+where,
		append([]any{id}, args...)...).Scan(&sha, &name, &format)
	if err != nil {
		writeErr(w, 404, "font not found")
		return
	}
	ct := map[string]string{"ttf": "font/ttf", "otf": "font/otf", "ttc": "font/collection"}[format]
	if ct == "" {
		ct = "application/octet-stream"
	}
	if r.URL.Query().Get("download") == "1" {
		s.serveDownload(w, u, []int64{id}) // license rules apply
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeFile(w, r, s.blobPath(sha))
}

// ---- upload -----------------------------------------------------------------

type uploadResult struct {
	File    string   `json:"file"`
	Status  string   `json:"status"` // added, duplicate, error
	Faces   []string `json:"faces,omitempty"`
	Message string   `json:"message,omitempty"`
}

// upFile is one file of an upload (a form file or a zip entry).
type upFile struct {
	name string // display name (path inside zip)
	data []byte
	// appleDouble marks a "__MACOSX/…/._Name" zip entry: the resource fork
	// of a Mac file (font suitcases, LWFN), silently ignored if no font.
	appleDouble bool
}

// extraFile is a companion stored alongside a font (Type 1 metrics).
type extraFile struct {
	SHA256 string `json:"sha256"`
	Ext    string `json:"ext"`
	Size   int    `json:"size"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, u *authUser) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadMB<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected multipart/form-data")
		return
	}
	var collection int64
	var results []uploadResult
	var files []upFile
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, "upload failed: "+err.Error())
			return
		}
		if p.FormName() == "collection_id" {
			b, _ := io.ReadAll(io.LimitReader(p, 32))
			collection, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			continue
		}
		if p.FileName() == "" {
			continue
		}
		fs, errRes := s.readPart(p)
		files = append(files, fs...)
		results = append(results, errRes...)
	}
	res, added := s.ingestBatch(files, u)
	results = append(results, res...)
	if collection > 0 && len(added) > 0 {
		for _, id := range added {
			s.db.Exec(`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id) VALUES (?, ?)`, collection, id)
		}
	}
	writeJSON(w, 200, map[string]any{"results": results, "font_ids": added})
}

// readPart reads a form file; zip archives are expanded.
func (s *Server) readPart(p *multipart.Part) ([]upFile, []uploadResult) {
	name := filepath.Base(strings.ReplaceAll(p.FileName(), "\\", "/"))
	if strings.ToLower(filepath.Ext(name)) != ".zip" {
		data, err := io.ReadAll(io.LimitReader(p, maxFontFile+1))
		if err != nil {
			return nil, []uploadResult{{File: name, Status: "error", Message: err.Error()}}
		}
		if len(data) > maxFontFile {
			return nil, []uploadResult{{File: name, Status: "error", Message: "file too large"}}
		}
		return []upFile{{name: name, data: data}}, nil
	}
	tmp, err := os.CreateTemp(filepath.Join(s.cfg.DataDir, "tmp"), "upload-*.zip")
	if err != nil {
		return nil, []uploadResult{{File: name, Status: "error", Message: "server storage error"}}
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, p)
	if err != nil {
		return nil, []uploadResult{{File: name, Status: "error", Message: err.Error()}}
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return nil, []uploadResult{{File: name, Status: "error", Message: "not a valid zip file"}}
	}
	var files []upFile
	var errs []uploadResult
	for _, zf := range zr.File {
		zn := zf.Name
		if zf.FileInfo().IsDir() || zf.UncompressedSize64 == 0 {
			continue
		}
		base := path.Base(zn)
		ad := strings.HasPrefix(base, "._")
		if strings.Contains(zn, "__MACOSX/") && !ad {
			continue
		}
		if zf.UncompressedSize64 > maxFontFile {
			errs = append(errs, uploadResult{File: zn, Status: "error", Message: "file too large"})
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			errs = append(errs, uploadResult{File: zn, Status: "error", Message: err.Error()})
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxFontFile+1))
		rc.Close()
		if err != nil {
			errs = append(errs, uploadResult{File: zn, Status: "error", Message: err.Error()})
			continue
		}
		if ad {
			zn = path.Join(path.Dir(strings.Replace(zn, "__MACOSX/", "", 1)), strings.TrimPrefix(base, "._"))
		}
		files = append(files, upFile{name: zn, data: data, appleDouble: ad})
	}
	return files, errs
}

func companionKey(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
}

// ingestBatch imports all files of one upload. PostScript Type 1 metrics
// (.pfm/.afm/.inf) are matched to their font by file name.
func (s *Server) ingestBatch(files []upFile, u *authUser) ([]uploadResult, []int64) {
	companions := map[string]map[string][]byte{} // key → ext → data
	var fonts []upFile
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.name))
		if fontmeta.CompanionExts[ext] && !f.appleDouble {
			k := companionKey(f.name)
			if companions[k] == nil {
				companions[k] = map[string][]byte{}
			}
			companions[k][ext] = f.data
			continue
		}
		fonts = append(fonts, f)
	}
	used := map[string]bool{}
	var results []uploadResult
	var ids []int64
	found := false
	for _, f := range fonts {
		decoded, err := fontmeta.Decode(f.name, f.data)
		if errors.Is(err, fontmeta.ErrNotFont) {
			if !f.appleDouble && len(files) == 1 {
				results = append(results, uploadResult{File: f.name, Status: "error",
					Message: "not a font file (supported: TrueType, OpenType, WOFF/WOFF2/EOT, Mac .dfont and suitcases, PostScript Type 1, Windows .fon/.fnt, X11 .pcf/.bdf)"})
			}
			continue
		}
		if err != nil {
			if !f.appleDouble {
				results = append(results, uploadResult{File: f.name, Status: "error", Message: err.Error()})
			}
			continue
		}
		found = true
		key := companionKey(f.name)
		for _, df := range decoded {
			var extras map[string][]byte
			if df.Kind == fontmeta.KindType1 {
				extras = companions[key]
				used[key] = true
			}
			res, newIDs := s.storeFont(f.name, df, extras, u)
			results = append(results, res)
			ids = append(ids, newIDs...)
		}
	}
	for k, c := range companions {
		if !used[k] {
			for ext := range c {
				results = append(results, uploadResult{File: k + ext, Status: "error",
					Message: "metrics file without its font – upload it together with the matching .pfb or .pfa"})
			}
		}
	}
	if !found && len(results) == 0 {
		results = append(results, uploadResult{File: "upload", Status: "error", Message: "no font files found"})
	}
	return results, ids
}

// storeFile writes a blob into the content-addressed store.
func (s *Server) storeFile(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	dst := s.blobPath(sha)
	if _, err := os.Stat(dst); err == nil {
		return sha, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.cfg.DataDir, "tmp"), "font-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, bytes.NewReader(data))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return sha, nil
}

var kindLabels = map[fontmeta.Kind]string{
	fontmeta.KindType1: "PostScript Type 1", fontmeta.KindWinFNT: "Windows bitmap font",
	fontmeta.KindPCF: "X11 bitmap font", fontmeta.KindBDF: "X11 bitmap font",
}

func (s *Server) storeFont(uploadName string, df fontmeta.File, extras map[string][]byte, u *authUser) (uploadResult, []int64) {
	res := uploadResult{File: uploadName}
	if df.Source != "" && !strings.EqualFold(uploadName, df.Name) {
		res.File = uploadName + " → " + df.Name
	}
	sha, err := s.storeFile(df.Data)
	if err != nil {
		s.log.Error("store font", "err", err)
		res.Status, res.Message = "error", "storage error"
		return res, nil
	}

	var exists int
	s.db.QueryRow(`SELECT COUNT(*) FROM fonts WHERE sha256 = ?`, sha).Scan(&exists)
	if exists > 0 {
		res.Status, res.Message = "duplicate", "already in library"
		var ids []int64
		rows, _ := s.db.Query(`SELECT id FROM fonts WHERE sha256 = ?`, sha)
		for rows != nil && rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		if rows != nil {
			rows.Close()
		}
		return res, ids
	}

	// companions (Type 1 metrics)
	ex := []extraFile{}
	hasPFM := false
	faces := df.Faces
	for ext, data := range extras {
		esha, err := s.storeFile(data)
		if err != nil {
			res.Status, res.Message = "error", "storage error"
			return res, nil
		}
		ex = append(ex, extraFile{SHA256: esha, Ext: ext, Size: len(data)})
		if ext == ".pfm" {
			hasPFM = true
		}
	}
	exJSON, _ := json.Marshal(ex)
	platforms := strings.Join(df.Kind.Platforms(hasPFM), ",")

	clean := sanitizeFilename(df.Name, df.Format)
	var ids []int64
	tx, err := s.db.Begin()
	if err != nil {
		res.Status, res.Message = "error", "database error"
		return res, nil
	}
	for i, f := range faces {
		r, err := tx.Exec(`INSERT INTO fonts (sha256, face_index, filename, format, size, family, style, full_name, postscript_name,
			version, designer, manufacturer, copyright, license_text, license_url, weight, italic, fs_type, uploaded_by, created_at,
			kind, platforms, source_format, extras)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			sha, i, clean, df.Format, len(df.Data), f.Family, f.Style, f.FullName, f.PostScriptName,
			f.Version, f.Designer, f.Manufacturer, f.Copyright, f.License, f.LicenseURL, f.Weight, f.Italic, f.FSType, u.ID, now(),
			string(df.Kind), platforms, df.Source, string(exJSON))
		if err != nil {
			tx.Rollback()
			res.Status, res.Message = "error", "database error"
			s.log.Error("insert font", "err", err)
			return res, nil
		}
		id, _ := r.LastInsertId()
		ids = append(ids, id)
		res.Faces = append(res.Faces, f.FullName)
	}
	if err := tx.Commit(); err != nil {
		res.Status, res.Message = "error", "database error"
		return res, nil
	}
	res.Status = "added"
	var notes []string
	if n, _, err := s.autoLicense(ids); err != nil {
		s.log.Warn("license detection", "err", err)
	} else if n > 0 {
		var name string
		s.db.QueryRow(`SELECT l.name FROM fonts f JOIN licenses l ON l.id = f.license_id WHERE f.id = ?`, ids[0]).Scan(&name)
		notes = append(notes, "license: "+name)
	}
	if df.Source != "" && df.Kind == fontmeta.KindSFNT {
		notes = append(notes, "converted from "+strings.ToUpper(df.Source)+" to "+strings.ToUpper(df.Format))
	}
	if label := kindLabels[df.Kind]; label != "" {
		notes = append(notes, label+" – installs on "+platformList(platforms))
		if df.Kind == fontmeta.KindType1 && !hasPFM {
			notes = append(notes, "add the matching .pfm to install it on Windows too")
		}
	}
	var dupes []string
	for _, f := range faces {
		if f.PostScriptName == "" {
			continue
		}
		var other string
		if s.db.QueryRow(`SELECT full_name || ' (v' || version || ')' FROM fonts WHERE postscript_name = ? AND sha256 != ? LIMIT 1`,
			f.PostScriptName, sha).Scan(&other) == nil {
			dupes = append(dupes, other)
		}
	}
	if len(dupes) > 0 {
		notes = append(notes, "same PostScript name as "+strings.Join(dupes, ", ")+" – only one of them can be active at a time")
	}
	res.Message = strings.Join(notes, "; ")
	s.audit(u, "font.upload", "%s: %s", clean, strings.Join(res.Faces, ", "))
	return res, ids
}

func platformList(p string) string {
	if p == "" {
		return "Windows, macOS and Linux"
	}
	names := map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}
	var out []string
	for _, x := range strings.Split(p, ",") {
		out = append(out, names[x])
	}
	if len(out) == 1 {
		return out[0] + " only"
	}
	return strings.Join(out, " and ")
}

func sanitizeFilename(name, format string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = "font"
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out + "." + format
}

// ---- bulk update / delete ----------------------------------------------------

func (s *Server) handleBulkUpdateFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		FontIDs   []int64 `json:"font_ids"`
		LicenseID *int64  `json:"license_id"` // null or 0 clears
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.FontIDs) == 0 {
		writeErr(w, 400, "font_ids required")
		return
	}
	var lic any
	if req.LicenseID != nil && *req.LicenseID > 0 {
		lic = *req.LicenseID
	}
	args := append([]any{lic}, int64Args(req.FontIDs)...)
	if _, err := s.db.Exec(`UPDATE fonts SET license_id = ? WHERE id IN (`+placeholders(len(req.FontIDs))+`)`, args...); err != nil {
		s.internalErr(w, err)
		return
	}
	s.audit(u, "font.license", "%d fonts → license %v", len(req.FontIDs), lic)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		FontIDs []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.FontIDs) == 0 {
		writeErr(w, 400, "font_ids required")
		return
	}
	ph := placeholders(len(req.FontIDs))
	args := int64Args(req.FontIDs)
	var shas []string
	var names []string
	var extraShas []string
	rows, err := s.db.Query(`SELECT DISTINCT sha256, full_name, extras FROM fonts WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	for rows.Next() {
		var sha, n, ex string
		rows.Scan(&sha, &n, &ex)
		shas = append(shas, sha)
		names = append(names, n)
		var extras []extraFile
		if json.Unmarshal([]byte(ex), &extras) == nil {
			for _, e := range extras {
				extraShas = append(extraShas, e.SHA256)
			}
		}
	}
	rows.Close()
	if _, err := s.db.Exec(`DELETE FROM fonts WHERE id IN (`+ph+`)`, args...); err != nil {
		s.internalErr(w, err)
		return
	}
	// remove blobs no longer referenced (e.g. all faces of a TTC deleted, or
	// the metrics files of a Type 1 font)
	for _, sha := range append(shas, extraShas...) {
		s.removeBlobIfUnused(sha)
	}
	s.audit(u, "font.delete", "%s", strings.Join(names, ", "))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- activation --------------------------------------------------------------

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		FontIDs []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.FontIDs) == 0 {
		writeErr(w, 400, "font_ids required")
		return
	}
	blocked, refused := s.activationRefusals(u.ID, req.FontIDs)
	var ids []int64
	for _, id := range req.FontIDs {
		if !blocked[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, 409, "Can't activate: "+strings.Join(refused, "; ")+". Ask an administrator.")
		return
	}
	where, vargs := visibleClause(u)
	args := append([]any{u.ID, now()}, int64Args(ids)...) // user_id, created_at, ids…
	args = append(args, vargs...)
	res, err := s.db.Exec(`INSERT OR IGNORE INTO activations (user_id, font_id, created_at)
		SELECT ?, f.id, ? FROM fonts f WHERE f.id IN (`+placeholders(len(ids))+`) AND `+where, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	n, _ := res.RowsAffected()
	replaced := s.resolveConflicts(u.ID, ids)
	if n > 0 {
		s.audit(u, "font.activate", "%d font(s)", n)
		s.usageChanged()
	}
	if refused == nil {
		refused = []string{}
	}
	writeJSON(w, 200, map[string]any{"activated": n, "replaced": replaced, "refused": refused})
}

func (s *Server) handleDeactivate(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		FontIDs []int64 `json:"font_ids"`
		All     bool    `json:"all"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var res sql.Result
	var err error
	if req.All {
		res, err = s.db.Exec(`DELETE FROM activations WHERE user_id = ?`, u.ID)
	} else {
		args := append([]any{u.ID}, int64Args(req.FontIDs)...)
		res, err = s.db.Exec(`DELETE FROM activations WHERE user_id = ? AND font_id IN (`+placeholders(len(req.FontIDs))+`)`, args...)
	}
	if err != nil {
		s.internalErr(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.audit(u, "font.deactivate", "%d font(s)", n)
		s.usageChanged()
	}
	// fonts the organisation installs stay, whatever the user switched off
	var kept int64
	if req.All {
		s.db.QueryRow(`SELECT COUNT(DISTINCT font_id) FROM required_fonts WHERE user_id = ?`, u.ID).Scan(&kept)
	} else if len(req.FontIDs) > 0 {
		s.db.QueryRow(`SELECT COUNT(DISTINCT font_id) FROM required_fonts WHERE user_id = ? AND font_id IN (`+placeholders(len(req.FontIDs))+`)`,
			append([]any{u.ID}, int64Args(req.FontIDs)...)...).Scan(&kept)
	}
	writeJSON(w, 200, map[string]int64{"deactivated": n, "kept": kept})
}

// resolveConflicts keeps at most one active font per PostScript name for a
// user: fonts just activated win over previously active ones, and among the
// requested ones the most recently uploaded wins. Two files with the same
// PostScript name installed side by side confuse applications and
// documents.
func (s *Server) resolveConflicts(userID int64, requested []int64) int64 {
	if len(requested) == 0 {
		return 0
	}
	ph := placeholders(len(requested))
	args := append([]any{userID}, int64Args(requested)...)
	args = append(args, int64Args(requested)...)
	r1, err := s.db.Exec(`DELETE FROM activations WHERE user_id = ? AND font_id IN (
		SELECT f2.id FROM fonts f1 JOIN fonts f2 ON f2.postscript_name = f1.postscript_name AND f2.sha256 != f1.sha256
		WHERE f1.id IN (`+ph+`) AND f1.postscript_name != '' AND f2.id NOT IN (`+ph+`))`, args...)
	if err != nil {
		s.log.Error("resolve conflicts", "err", err)
		return 0
	}
	n1, _ := r1.RowsAffected()
	args = append([]any{userID}, int64Args(requested)...)
	args = append(args, int64Args(requested)...)
	r2, err := s.db.Exec(`DELETE FROM activations WHERE user_id = ? AND font_id IN (
		SELECT f1.id FROM fonts f1 JOIN fonts f2 ON f2.postscript_name = f1.postscript_name AND f2.sha256 != f1.sha256 AND f2.id > f1.id
		WHERE f1.id IN (`+ph+`) AND f2.id IN (`+ph+`) AND f1.postscript_name != '')`, args...)
	if err != nil {
		return n1
	}
	n2, _ := r2.RowsAffected()
	return n1 + n2
}
