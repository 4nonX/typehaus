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
	FamilyAuto     string   `json:"family_auto"`  // family as detected from the file
	StyleAuto      string   `json:"style_auto"`   // style as detected from the file
	NamesManual    bool     `json:"names_manual"` // family/style set by hand
}

const fontCols = `f.id, f.sha256, f.face_index, f.filename, f.format, f.size, f.family, f.style, f.full_name,
	f.postscript_name, f.version, f.designer, f.manufacturer, f.weight, f.italic, f.fs_type, f.license_id, f.created_at,
	f.family_auto, f.style_auto, f.names_manual`

func scanFont(sc interface{ Scan(...any) error }, f *fontRow) error {
	var lic sql.NullInt64
	err := sc.Scan(&f.ID, &f.SHA256, &f.FaceIndex, &f.Filename, &f.Format, &f.Size, &f.Family, &f.Style, &f.FullName,
		&f.PostScriptName, &f.Version, &f.Designer, &f.Manufacturer, &f.Weight, &f.Italic, &f.FSType, &lic, &f.CreatedAt,
		&f.FamilyAuto, &f.StyleAuto, &f.NamesManual)
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
			&f.FamilyAuto, &f.StyleAuto, &f.NamesManual,
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
	if u.Token != nil { // a token is not a person: nothing is switched on "for" it
		for _, f := range out {
			f.Activated, f.Required, f.RequiredBy = false, false, nil
		}
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
	for _, f := range out {
		f.normalize()
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
	var copyright, licText, licURL, vendorID, trademark string
	row := s.db.QueryRow(`SELECT `+fontCols+`, f.copyright, f.license_text, f.license_url, f.vendor_id, f.trademark FROM fonts f WHERE f.id = ? AND `+where,
		append([]any{id}, args...)...)
	var lic sql.NullInt64
	err := row.Scan(&f.ID, &f.SHA256, &f.FaceIndex, &f.Filename, &f.Format, &f.Size, &f.Family, &f.Style, &f.FullName,
		&f.PostScriptName, &f.Version, &f.Designer, &f.Manufacturer, &f.Weight, &f.Italic, &f.FSType, &lic, &f.CreatedAt,
		&f.FamilyAuto, &f.StyleAuto, &f.NamesManual,
		&copyright, &licText, &licURL, &vendorID, &trademark)
	if err != nil {
		writeErr(w, 404, "font not found")
		return
	}
	if lic.Valid {
		f.LicenseID = &lic.Int64
	}
	f.normalize()
	writeJSON(w, 200, fontDetail{Font: &f, Copyright: copyright, LicenseText: licText, LicenseURL: licURL, VendorID: vendorID, Trademark: trademark})
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

// lists are always arrays in the API, never null
func (f *fontRow) normalize() {
	if f.RequiredBy == nil {
		f.RequiredBy = []string{}
	}
	if f.Collections == nil {
		f.Collections = []int64{}
	}
}

// fontDetail is GET /fonts/{id}: the font and what its file says about
// origin and terms.
type fontDetail struct {
	Font        *fontRow `json:"font"`
	Copyright   string   `json:"copyright"`
	LicenseText string   `json:"license_text"`
	LicenseURL  string   `json:"license_url"`
	VendorID    string   `json:"vendor_id"`
	Trademark   string   `json:"trademark"`
}

type uploadResult struct {
	File    string   `json:"file"`
	Status  string   `json:"status"` // added, duplicate, error
	Faces   []string `json:"faces,omitempty"`
	Message string   `json:"message,omitempty"`
	// Updates lists styles that share a PostScript name with a font already
	// in the library (usually another version), so the admin can replace it.
	Updates []fontUpdate `json:"updates,omitempty"`
	// SameFont is set when the file was skipped as the same font in a
	// different file; uploading again with force adds it anyway.
	SameFont bool `json:"same_font,omitempty"`
}

type fontUpdate struct {
	NewID      int64  `json:"new_id"`
	OldID      int64  `json:"old_id"`
	Name       string `json:"name"`
	NewVersion string `json:"new_version"`
	OldVersion string `json:"old_version"`
	Newer      bool   `json:"newer"` // the uploaded version is higher than the existing one
	NewVendor  string `json:"new_vendor,omitempty"`
	OldVendor  string `json:"old_vendor,omitempty"`
	// OtherVendor: the two come from different foundries, so this is not an
	// update but another font that happens to share the name.
	OtherVendor bool `json:"other_vendor,omitempty"`
	// LicenseChanged: the license notice in the files differs.
	LicenseChanged bool `json:"license_changed,omitempty"`
}

// vendorOf describes who made a font: the manufacturer name, else the OS/2
// vendor ID. Placeholder IDs that font editors write by default say nothing.
func vendorOf(vendorID, manufacturer string) string {
	if m := strings.TrimSpace(manufacturer); m != "" {
		return m
	}
	return realVendorID(vendorID)
}

func realVendorID(id string) string {
	id = strings.TrimSpace(id)
	switch strings.ToUpper(id) {
	case "", "NONE", "UKWN", "PFED", "XXXX", "????", "ALTS", "MACR", "UNKN":
		return ""
	}
	return id
}

// differs is true when both values are known and not the same.
func differs(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && !strings.EqualFold(a, b)
}

type fontOrigin struct{ vendorID, manufacturer, licenseText, licenseURL string }

func (s *Server) fontOrigin(id int64) (o fontOrigin) {
	s.db.QueryRow(`SELECT COALESCE(vendor_id,''), manufacturer, license_text, license_url FROM fonts WHERE id = ?`, id).
		Scan(&o.vendorID, &o.manufacturer, &o.licenseText, &o.licenseURL)
	return o
}

// otherVendor is true when two fonts provably come from different foundries.
// The manufacturer name decides when both files have one (vendor IDs change
// when foundries are bought, the name usually does not); otherwise the
// registered vendor IDs are compared.
func otherVendor(a, b fontOrigin) bool {
	if strings.TrimSpace(a.manufacturer) != "" && strings.TrimSpace(b.manufacturer) != "" {
		return differs(a.manufacturer, b.manufacturer)
	}
	return differs(realVendorID(a.vendorID), realVendorID(b.vendorID))
}

func licenseChanged(a, b fontOrigin) bool {
	return differs(a.licenseURL, b.licenseURL) || differs(a.licenseText, b.licenseText)
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
	force := false // "upload anyway": skip the same-font check (not the identical-file check)
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
		if p.FormName() == "force" {
			b, _ := io.ReadAll(io.LimitReader(p, 8))
			force = strings.TrimSpace(string(b)) == "1"
			continue
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
	res, added := s.ingestBatch(files, u, force)
	results = append(results, res...)
	if collection > 0 && len(added) > 0 {
		for _, id := range added {
			s.db.Exec(`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id) VALUES (?, ?)`, collection, id)
		}
	}
	if len(added) > 0 {
		s.emit("font.added", map[string]any{"fonts": s.eventFonts(added), "by": u.Username})
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
	return s.readZip(name, p)
}

// readZip expands a zip archive into its files.
func (s *Server) readZip(name string, src io.Reader) ([]upFile, []uploadResult) {
	tmp, err := os.CreateTemp(filepath.Join(s.cfg.DataDir, "tmp"), "upload-*.zip")
	if err != nil {
		return nil, []uploadResult{{File: name, Status: "error", Message: "server storage error"}}
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, src)
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
func (s *Server) ingestBatch(files []upFile, u *authUser, force bool) ([]uploadResult, []int64) {
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
			res, newIDs := s.storeFont(f.name, df, extras, u, force)
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

func (s *Server) storeFont(uploadName string, df fontmeta.File, extras map[string][]byte, u *authUser, force bool) (uploadResult, []int64) {
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

	// The same font in a different file (a WOFF2 converted to TTF, a re-saved
	// or re-signed copy) is recognised by its content fingerprint, not by its
	// name: families repeat names across weights, widths and optical sizes.
	skip := map[int]bool{}
	var known []int64
	var knownNames []string
	for i, f := range df.Faces {
		if force {
			break
		}
		if id, name, ok := s.sameFont(f); ok {
			skip[i] = true
			known = append(known, id)
			knownNames = append(knownNames, name)
		}
	}
	if len(skip) == len(df.Faces) && len(skip) > 0 {
		s.removeBlobIfUnused(sha)
		res.Status, res.Message = "duplicate", "already in library as "+strings.Join(knownNames, ", ")+" (same font, different file)"
		res.SameFont = true
		return res, known
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
		if skip[i] {
			continue // this face of a collection is already in the library
		}
		r, err := tx.Exec(`INSERT INTO fonts (sha256, face_index, filename, format, size, family, style, full_name, postscript_name,
			version, designer, manufacturer, copyright, license_text, license_url, weight, italic, fs_type, uploaded_by, created_at,
			kind, platforms, source_format, extras, vendor_id, trademark, meta_checked, fingerprint, names_v, family_auto, style_auto)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,1,?,?)`,
			sha, i, clean, df.Format, len(df.Data), f.Family, f.Style, f.FullName, f.PostScriptName,
			f.Version, f.Designer, f.Manufacturer, f.Copyright, f.License, f.LicenseURL, f.Weight, f.Italic, f.FSType, u.ID, now(),
			string(df.Kind), platforms, df.Source, string(exJSON), f.VendorID, f.Trademark, f.Fingerprint, f.Family, f.Style)
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
	for i, f := range faces {
		if f.PostScriptName == "" || skip[i] {
			continue
		}
		var other string
		if s.db.QueryRow(`SELECT full_name || ' (v' || version || ')' FROM fonts WHERE postscript_name = ? AND sha256 != ? LIMIT 1`,
			f.PostScriptName, sha).Scan(&other) == nil {
			dupes = append(dupes, other)
		}
	}
	if len(dupes) > 0 {
		notes = append(notes, "same PostScript name as "+strings.Join(dupes, ", ")+" (a different version or build) – only one of them can be active at a time")
		res.Updates = s.versionPairs(ids)
	}
	if len(knownNames) > 0 {
		notes = append(notes, "already in library, skipped: "+strings.Join(knownNames, ", "))
	}
	res.Message = strings.Join(notes, "; ")
	s.audit(u, "font.upload", "%s: %s", clean, strings.Join(res.Faces, ", "))
	return res, append(ids, known...)
}

// sameFont finds a face already in the library that is the same font as f,
// by content fingerprint. Names only pick the candidates (same PostScript
// name, or same family and style when there is none); whether it is the same
// font is decided by the fingerprint, so different weights, widths, optical
// sizes or versions sharing a family name are never mistaken for duplicates.
// Older entries without a fingerprint get one computed from their file now.
func (s *Server) sameFont(f fontmeta.Face) (int64, string, bool) {
	if f.Fingerprint == "" {
		return 0, "", false
	}
	rows, err := s.db.Query(`SELECT id, sha256, face_index, fingerprint, full_name, version FROM fonts
		WHERE (postscript_name = ?1 AND ?1 != '') OR (family = ?2 COLLATE NOCASE AND style = ?3 COLLATE NOCASE)`,
		f.PostScriptName, f.Family, f.Style)
	if err != nil {
		return 0, "", false
	}
	type cand struct {
		id        int64
		sha, fp   string
		index     int
		name, ver string
	}
	var cs []cand
	for rows.Next() {
		var c cand
		rows.Scan(&c.id, &c.sha, &c.index, &c.fp, &c.name, &c.ver)
		cs = append(cs, c)
	}
	rows.Close()
	for _, c := range cs {
		if c.fp == "" || c.fp != "-" && !strings.HasPrefix(c.fp, fontmeta.FingerprintVersion) {
			c.fp = ""
			if b, err := os.ReadFile(s.blobPath(c.sha)); err == nil {
				if info, err := fontmeta.Parse(b); err == nil && c.index < len(info.Faces) {
					c.fp = info.Faces[c.index].Fingerprint
				}
			}
			if c.fp == "" {
				c.fp = "-" // not a TrueType/OpenType face: never compared again
			}
			s.db.Exec(`UPDATE fonts SET fingerprint = ? WHERE id = ?`, c.fp, c.id)
		}
		if c.fp == f.Fingerprint {
			name := c.name
			if c.ver != "" {
				name += " (v" + c.ver + ")"
			}
			return c.id, name, true
		}
	}
	return 0, "", false
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
	s.emit("font.license_changed", map[string]any{"fonts": s.eventFonts(req.FontIDs), "license_id": lic, "by": u.Username})
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
	gone := s.eventFonts(req.FontIDs) // before they are gone
	names, err := s.deleteFonts(req.FontIDs)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	s.audit(u, "font.delete", "%s", strings.Join(names, ", "))
	if len(gone) > 0 {
		s.emit("font.deleted", map[string]any{"fonts": gone, "by": u.Username})
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteFonts removes fonts and the files no other entry uses any more.
func (s *Server) deleteFonts(ids []int64) ([]string, error) {
	ph := placeholders(len(ids))
	args := int64Args(ids)
	var shas []string
	var names []string
	var extraShas []string
	rows, err := s.db.Query(`SELECT DISTINCT sha256, full_name, extras FROM fonts WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	// remove blobs no longer referenced (e.g. all faces of a TTC deleted, or
	// the metrics files of a Type 1 font)
	for _, sha := range append(shas, extraShas...) {
		s.removeBlobIfUnused(sha)
	}
	return names, nil
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
		writeErrCode(w, 409, "license_refused", "Can't activate: "+strings.Join(refused, "; ")+". Ask an administrator.")
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

// refreshFamilyNames re-reads family and style from the files of fonts that
// were named before legacy family names were folded into their family
// (fontmeta.legacyFamily), so "Helvetica Light" joins "Helvetica". It runs
// once per font: names_v marks the fonts already done.
func (s *Server) refreshFamilyNames() {
	rows, err := s.db.Query(`SELECT id, sha256, face_index FROM fonts WHERE names_v = 0 AND kind = 'sfnt'`)
	if err != nil {
		s.log.Warn("refresh family names", "err", err)
		return
	}
	type face struct {
		id    int64
		sha   string
		index int
	}
	var todo []face
	for rows.Next() {
		var f face
		rows.Scan(&f.id, &f.sha, &f.index)
		todo = append(todo, f)
	}
	rows.Close()
	changed := 0
	parsed := map[string]*fontmeta.Info{}
	for _, f := range todo {
		info, seen := parsed[f.sha]
		if !seen {
			if b, err := os.ReadFile(s.blobPath(f.sha)); err == nil {
				info, _ = fontmeta.Parse(b)
			}
			parsed[f.sha] = info
		}
		if info != nil && f.index < len(info.Faces) {
			fc := info.Faces[f.index]
			res, err := s.db.Exec(`UPDATE fonts SET family_auto = ?1, style_auto = ?2, names_v = 1,
				family = CASE WHEN names_manual = 1 THEN family ELSE ?1 END,
				style = CASE WHEN names_manual = 1 THEN style ELSE ?2 END
				WHERE id = ?3 AND (family_auto != ?1 OR style_auto != ?2 OR (names_manual = 0 AND (family != ?1 OR style != ?2)))`,
				fc.Family, fc.Style, f.id)
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					changed++
				}
			}
		}
		s.db.Exec(`UPDATE fonts SET names_v = 1 WHERE id = ?`, f.id)
	}
	if changed > 0 {
		s.log.Info("family names updated", "fonts", changed)
	}
}

// handleSetFamily resolves what automatic grouping gets wrong: it moves the
// selected styles into a family (merge into an existing one, or split them off
// under a new name) and, for a single style, can rename the style too. Names
// set here are kept: re-reading the files never overwrites them. reset puts
// the selected styles back to the family and style detected from their files.
func (s *Server) handleSetFamily(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		FontIDs []int64 `json:"font_ids"`
		Family  string  `json:"family"`
		Style   string  `json:"style"` // only with a single font; "" keeps the style
		Reset   bool    `json:"reset"`
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
	if req.Reset {
		if _, err := s.db.Exec(`UPDATE fonts SET family = CASE WHEN family_auto != '' THEN family_auto ELSE family END,
			style = CASE WHEN style_auto != '' THEN style_auto ELSE style END, names_manual = 0 WHERE id IN (`+ph+`)`, args...); err != nil {
			s.internalErr(w, err)
			return
		}
		s.audit(u, "font.family", "%d fonts → detected family", len(req.FontIDs))
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	family := strings.Join(strings.Fields(req.Family), " ")
	style := strings.Join(strings.Fields(req.Style), " ")
	if family == "" || len(family) > 200 || len(style) > 200 {
		writeErr(w, 400, "a family name of up to 200 characters is required")
		return
	}
	if style != "" && len(req.FontIDs) != 1 {
		writeErr(w, 400, "a style name can only be set for a single style")
		return
	}
	q := `UPDATE fonts SET family = ?, names_manual = 1`
	vals := []any{family}
	if style != "" {
		q += `, style = ?`
		vals = append(vals, style)
	}
	if _, err := s.db.Exec(q+` WHERE id IN (`+ph+`)`, append(vals, args...)...); err != nil {
		s.internalErr(w, err)
		return
	}
	s.audit(u, "font.family", "%d fonts → %s", len(req.FontIDs), family)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// versionPairs lists, for newly added fonts, the fonts already in the library
// with the same PostScript name in another file: usually an older or newer
// version of the same style.
func (s *Server) versionPairs(newIDs []int64) []fontUpdate {
	if len(newIDs) == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT n.id, o.id, n.full_name, n.version, o.version,
		COALESCE(n.vendor_id,''), n.manufacturer, n.license_text, n.license_url,
		COALESCE(o.vendor_id,''), o.manufacturer, o.license_text, o.license_url FROM fonts n
		JOIN fonts o ON o.postscript_name = n.postscript_name AND o.sha256 != n.sha256 AND o.id != n.id
		WHERE n.id IN (`+placeholders(len(newIDs))+`) AND n.postscript_name != '' ORDER BY n.id, o.id`, int64Args(newIDs)...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []fontUpdate
	for rows.Next() {
		var p fontUpdate
		var n, o fontOrigin
		rows.Scan(&p.NewID, &p.OldID, &p.Name, &p.NewVersion, &p.OldVersion,
			&n.vendorID, &n.manufacturer, &n.licenseText, &n.licenseURL,
			&o.vendorID, &o.manufacturer, &o.licenseText, &o.licenseURL)
		p.Newer = compareVersions(p.NewVersion, p.OldVersion) > 0
		p.NewVendor, p.OldVendor = vendorOf(n.vendorID, n.manufacturer), vendorOf(o.vendorID, o.manufacturer)
		p.OtherVendor = otherVendor(n, o)
		p.LicenseChanged = licenseChanged(n, o)
		out = append(out, p)
	}
	return out
}

// compareVersions compares font version strings such as "4.003", "Version
// 2.1;hotfix" or "1.10": the numbers are compared one by one, so 1.10 is
// newer than 1.9. It returns -1, 0 or 1.
func compareVersions(a, b string) int {
	num := func(s string) []int {
		var out []int
		cur, in := 0, false
		for _, r := range s {
			if r >= '0' && r <= '9' {
				cur, in = cur*10+int(r-'0'), true
				continue
			}
			if in {
				out = append(out, cur)
				cur, in = 0, false
			}
			if r == ';' || r == ' ' && len(out) > 0 {
				break // the rest is build information, not the version
			}
		}
		if in {
			out = append(out, cur)
		}
		return out
	}
	x, y := num(a), num(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			if p > q {
				return 1
			}
			return -1
		}
	}
	return 0
}

// handleReplaceFonts makes one version of a style the current one: the new
// font takes over the old one's license (unless it comes from another
// foundry: that license does not cover it), collections, activations and any
// names set by hand, then the old font is removed. Devices install the new
// file and drop the old one on their next sync.
func (s *Server) handleReplaceFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		Pairs []struct {
			OldID int64 `json:"old_id"`
			NewID int64 `json:"new_id"`
		} `json:"pairs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var old []int64
	replaced, unlicensed := 0, 0
	for _, p := range req.Pairs {
		if p.OldID == p.NewID || p.OldID <= 0 || p.NewID <= 0 {
			continue
		}
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM fonts WHERE id IN (?, ?)`, p.OldID, p.NewID).Scan(&n)
		if n != 2 {
			continue
		}
		// read before the transaction: the database has one connection
		foreign := otherVendor(s.fontOrigin(p.OldID), s.fontOrigin(p.NewID))
		var lic sql.NullInt64
		s.db.QueryRow(`SELECT license_id FROM fonts WHERE id = ?`, p.OldID).Scan(&lic)
		tx, err := s.db.Begin()
		if err != nil {
			s.internalErr(w, err)
			return
		}
		stmts := []string{
			`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id) SELECT collection_id, ?2 FROM collection_fonts WHERE font_id = ?1`,
			`INSERT OR IGNORE INTO activations (user_id, font_id, created_at) SELECT user_id, ?2, created_at FROM activations WHERE font_id = ?1`,
			`UPDATE fonts SET family = (SELECT family FROM fonts WHERE id = ?1), style = (SELECT style FROM fonts WHERE id = ?1), names_manual = 1
				WHERE id = ?2 AND (SELECT names_manual FROM fonts WHERE id = ?1) = 1`,
		}
		if foreign {
			if lic.Valid {
				unlicensed++
			}
		} else {
			stmts = append(stmts, `UPDATE fonts SET license_id = (SELECT license_id FROM fonts WHERE id = ?1) WHERE id = ?2 AND license_id IS NULL`)
		}
		for _, q := range stmts {
			if _, err := tx.Exec(q, p.OldID, p.NewID); err != nil {
				tx.Rollback()
				s.internalErr(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			s.internalErr(w, err)
			return
		}
		old = append(old, p.OldID)
		replaced++
	}
	if len(old) > 0 {
		names, err := s.deleteFonts(old)
		if err != nil {
			s.internalErr(w, err)
			return
		}
		s.audit(u, "font.replace", "%d replaced by a new version: %s", replaced, strings.Join(names, ", "))
	}
	writeJSON(w, 200, map[string]int{"replaced": replaced, "license_not_carried": unlicensed})
}
