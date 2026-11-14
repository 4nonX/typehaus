package server

import (
	"encoding/json"
	"os"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

// What the library filters by beyond names: the design classification, variable
// fonts and the width class. New fonts get them when they are added; fonts
// added before are read once in the background (class_v marks them done).
// Many fonts don't declare a classification – the free catalog's curated
// category fills those in, and wins for fonts imported from it.

var catalogClasses = map[string]bool{"sans-serif": true, "serif": true, "display": true, "handwriting": true, "monospace": true}

// refreshClasses reads class, variable and width from the files of fonts
// stored before these were, then applies the cached catalog's categories.
func (s *Server) refreshClasses() {
	rows, err := s.db.Query(`SELECT id, sha256, face_index FROM fonts WHERE class_v = 0 AND kind = 'sfnt'`)
	if err != nil {
		s.log.Warn("font classes", "err", err)
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
			width := fc.Width
			if width == 0 {
				width = 5
			}
			s.db.Exec(`UPDATE fonts SET class = CASE WHEN class = '' THEN ? ELSE class END, variable = ?, width = ?, class_v = 1 WHERE id = ?`,
				fc.Class, fc.Variable, width, f.id)
			continue
		}
		s.db.Exec(`UPDATE fonts SET class_v = 1 WHERE id = ?`, f.id)
	}
	s.db.Exec(`UPDATE fonts SET class_v = 1 WHERE class_v = 0`) // other formats: nothing to read
	if len(todo) > 0 {
		s.log.Info("font classes read", "fonts", len(todo))
	}
	if !s.cfg.Catalog {
		return
	}
	if b, err := os.ReadFile(s.catalogFile()); err == nil {
		var disk struct {
			Entries []*catalogEntry `json:"entries"`
		}
		if json.Unmarshal(b, &disk) == nil {
			s.applyCatalogClasses(disk.Entries)
		}
	}
}

// applyCatalogClasses gives fonts the free catalog's category: those
// imported from it always, others when they declare none themselves and
// their family has the same name as a catalog family.
func (s *Server) applyCatalogClasses(entries []*catalogEntry) {
	byID := map[string]string{}
	byKey := map[string]string{}
	for _, e := range entries {
		if !catalogClasses[e.Category] {
			continue
		}
		byID[e.ID] = e.Category
		if k := familyKey(e.Family); byKey[k] == "" {
			byKey[k] = e.Category
		}
	}
	rows, err := s.db.Query(`SELECT DISTINCT family, catalog_ref, class FROM fonts WHERE class = '' OR catalog_ref != ''`)
	if err != nil {
		return
	}
	type fam struct{ family, ref, class string }
	var fams []fam
	for rows.Next() {
		var f fam
		rows.Scan(&f.family, &f.ref, &f.class)
		fams = append(fams, f)
	}
	rows.Close()
	n := 0
	for _, f := range fams {
		if f.ref != "" {
			if c := byID[f.ref]; c != "" && c != f.class {
				res, _ := s.db.Exec(`UPDATE fonts SET class = ? WHERE catalog_ref = ? AND family = ?`, c, f.ref, f.family)
				if k, _ := res.RowsAffected(); k > 0 {
					n += int(k)
				}
			}
			continue
		}
		if c := byKey[familyKey(f.family)]; c != "" {
			res, _ := s.db.Exec(`UPDATE fonts SET class = ? WHERE family = ? AND class = '' AND catalog_ref = ''`, c, f.family)
			if k, _ := res.RowsAffected(); k > 0 {
				n += int(k)
			}
		}
	}
	if n > 0 {
		s.log.Info("font classes from the free catalog", "fonts", n)
	}
}
