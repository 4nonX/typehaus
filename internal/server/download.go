package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Direct downloads, for installing fonts the traditional way (double-click,
// Font Book, a design app's document fonts folder…). License rules apply:
// a license can forbid direct downloads or be for named people only, and an
// enforced license only lets users who hold a seat download, and nobody
// after it expired.

type dlFont struct {
	id                        int64
	sha, filename, family     string
	fullName, licText, licURL string
	extras                    []extraFile
	licID                     sql.NullInt64
}

type dlLicense struct {
	name   string
	reason string // why downloading is refused, "" = allowed
}

// downloadable resolves the requested fonts the user may see and splits
// them into allowed fonts and refusal reasons.
func (s *Server) downloadable(u *authUser, ids []int64) ([]dlFont, []string, error) {
	where, vargs := visibleClause(u)
	args := append([]any{u.ID, u.ID}, int64Args(ids)...)
	args = append(args, vargs...)
	rows, err := s.db.Query(`SELECT f.id, f.sha256, f.filename, f.family, f.full_name, f.license_text, f.license_url, f.extras,
		l.id, COALESCE(l.name, ''), COALESCE(l.downloadable, 1), COALESCE(l.enforce, 0), COALESCE(l.seats, 0), COALESCE(l.expires_on, ''), COALESCE(l.count_admins, 1),
		l.id IS NOT NULL AND EXISTS(SELECT 1 FROM user_fonts a JOIN fonts f2 ON f2.id = a.font_id WHERE f2.license_id = l.id AND a.user_id = ?),
		l.id IS NOT NULL AND EXISTS(SELECT 1 FROM license_users lu WHERE lu.license_id = l.id),
		l.id IS NOT NULL AND EXISTS(SELECT 1 FROM license_users lu WHERE lu.license_id = l.id AND lu.user_id = ?)
		FROM fonts f LEFT JOIN licenses l ON l.id = f.license_id
		WHERE f.id IN (`+placeholders(len(ids))+`) AND `+where+`
		ORDER BY f.family COLLATE NOCASE, f.italic, f.weight, f.style`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ok []dlFont
	lics := map[int64]*dlLicense{}
	var reasons []string
	nowT := time.Now()
	for rows.Next() {
		var f dlFont
		var extras, licName, expires string
		var allowDL, enforce, countAdmins, holder bool
		var seats int
		var named, mine bool
		if err := rows.Scan(&f.id, &f.sha, &f.filename, &f.family, &f.fullName, &f.licText, &f.licURL, &extras,
			&f.licID, &licName, &allowDL, &enforce, &seats, &expires, &countAdmins, &holder, &named, &mine); err != nil {
			return nil, nil, err
		}
		json.Unmarshal([]byte(extras), &f.extras)
		// admins are held to the license too, unless it says not to count them
		if u.Token != nil {
			holder = false // seats are held by people, not by tokens
			mine = false   // and a license for named people names people
		}
		if f.licID.Valid && !(u.IsAdmin && u.Token == nil && !countAdmins) {
			l := lics[f.licID.Int64]
			if l == nil {
				l = &dlLicense{name: licName}
				expired, _ := expiry(expires, nowT)
				switch {
				case named && !mine:
					l.reason = fmt.Sprintf("the license “%s” is for named people only", licName)
				case !allowDL:
					l.reason = fmt.Sprintf("the license “%s” does not allow downloading the font files", licName)
				case enforce && expired:
					l.reason = fmt.Sprintf("the license “%s” expired on %s", licName, expires)
				case enforce && seats > 0 && !holder:
					l.reason = fmt.Sprintf("the license “%s” is limited to seat holders – activate the font to take a free seat first", licName)
				}
				lics[f.licID.Int64] = l
				if l.reason != "" {
					reasons = append(reasons, l.reason)
				}
			}
			if l.reason != "" {
				continue
			}
		}
		ok = append(ok, f)
	}
	return ok, reasons, rows.Err()
}

func parseIDs(s string) []int64 {
	var ids []int64
	seen := map[int64]bool{}
	for _, p := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// handleDownloadFonts: GET /api/fonts/download?ids=1,2,3
func (s *Server) handleDownloadFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	ids := parseIDs(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		writeErr(w, 400, "ids required")
		return
	}
	if len(ids) > 5000 {
		writeErr(w, 400, "too many fonts in one download")
		return
	}
	s.serveDownload(w, u, ids)
}

func (s *Server) serveDownload(w http.ResponseWriter, u *authUser, ids []int64) {
	// an API token distributes fonts to another system: no admin exception
	if (!u.IsAdmin || u.Token != nil) && !s.cfg.Downloads {
		writeErrCode(w, 403, "downloads_disabled", "Direct downloads are switched off on this server. Activate the fonts instead – the typehaus app installs them for you.")
		return
	}
	fonts, refused, err := s.downloadable(u, ids)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if len(fonts) == 0 {
		if len(refused) > 0 {
			writeErrCode(w, 403, "license_refused", "Can't download: "+strings.Join(refused, "; ")+".")
		} else {
			writeErr(w, 404, "font not found")
		}
		return
	}
	if len(refused) > 0 {
		// partial download: tell the web UI what was left out
		w.Header().Set("X-Typehaus-Skipped", mime.QEncoding.Encode("utf-8", strings.Join(refused, "; ")))
	}
	// distinct files (all faces of a collection share one file)
	var files []dlFont
	seen := map[string]bool{}
	for _, f := range fonts {
		if !seen[f.sha] {
			seen[f.sha] = true
			files = append(files, f)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if len(files) == 1 && len(files[0].extras) == 0 {
		f := files[0]
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.filename}))
		fh, err := os.Open(s.blobPath(f.sha))
		if err != nil {
			writeErr(w, 404, "font file missing")
			return
		}
		defer fh.Close()
		if st, err := fh.Stat(); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		}
		io.Copy(w, fh)
		s.audit(u, "font.download", "%s", f.filename)
		return
	}

	families := map[string]bool{}
	for _, f := range files {
		families[f.family] = true
	}
	zipName := "typehaus-fonts.zip"
	if len(families) == 1 {
		zipName = sanitizeFilename(files[0].family, "zip")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": zipName}))
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	unique := func(name string) string {
		if !used[strings.ToLower(name)] {
			used[strings.ToLower(name)] = true
			return name
		}
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		for i := 2; ; i++ {
			n := fmt.Sprintf("%s (%d)%s", base, i, ext)
			if !used[strings.ToLower(n)] {
				used[strings.ToLower(n)] = true
				return n
			}
		}
	}
	addBlob := func(name, sha string) error {
		fh, err := os.Open(s.blobPath(sha))
		if err != nil {
			return err
		}
		defer fh.Close()
		zf, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, fh)
		return err
	}
	folder := func(fam string) string {
		if len(families) == 1 {
			return ""
		}
		return strings.TrimSuffix(sanitizeFilename(fam, "x"), ".x") + "/"
	}
	licTexts := map[string]string{} // folder -> embedded license text
	licIDs := map[string]map[int64]bool{}
	var names []string
	for _, f := range files {
		dir := folder(f.family)
		// Companion files keep the font's base name: Windows pairs .pfm and
		// .pfb by name.
		name := unique(dir + f.filename)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if err := addBlob(name, f.sha); err != nil {
			s.log.Error("download", "file", f.filename, "err", err)
			zw.Close()
			return
		}
		for _, e := range f.extras {
			if err := addBlob(unique(stem+e.Ext), e.SHA256); err != nil {
				s.log.Error("download", "file", f.filename+e.Ext, "err", err)
			}
		}
		names = append(names, f.filename)
		if _, ok := licTexts[dir]; !ok && (f.licText != "" || f.licURL != "") {
			licTexts[dir] = strings.TrimSpace(f.licText + "\n\n" + f.licURL)
		}
		if f.licID.Valid {
			if licIDs[dir] == nil {
				licIDs[dir] = map[int64]bool{}
			}
			licIDs[dir][f.licID.Int64] = true
		}
	}
	// license information travels with the fonts
	for dir, text := range licTexts {
		if zf, err := zw.Create(unique(dir + "LICENSE.txt")); err == nil {
			io.WriteString(zf, "License information embedded in the font files:\r\n\r\n"+strings.ReplaceAll(text, "\n", "\r\n")+"\r\n")
		}
	}
	for dir, lids := range licIDs {
		for lid := range lids {
			rows, err := s.db.Query(`SELECT sha256, filename FROM license_documents WHERE license_id = ? AND shared = 1`, lid)
			if err != nil {
				continue
			}
			type doc struct{ sha, name string }
			var docs []doc
			for rows.Next() {
				var d doc
				rows.Scan(&d.sha, &d.name)
				docs = append(docs, d)
			}
			rows.Close()
			for _, d := range docs {
				addBlob(unique(dir+"License/"+d.name), d.sha)
			}
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Warn("download", "err", err)
	}
	if len(names) > 6 {
		names = append(names[:6], fmt.Sprintf("… (%d files)", len(files)))
	}
	s.audit(u, "font.download", "%s", strings.Join(names, ", "))
}
