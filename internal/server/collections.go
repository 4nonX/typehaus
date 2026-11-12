package server

import (
	"net/http"
	"strings"
)

type collectionRow struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Everyone    bool    `json:"everyone"`
	GroupIDs    []int64 `json:"group_ids"`
	FontCount   int     `json:"font_count"`
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request, u *authUser) {
	q := `SELECT c.id, c.name, c.description, c.everyone,
		(SELECT COUNT(*) FROM collection_fonts cf WHERE cf.collection_id = c.id) FROM collections c`
	var args []any
	if !u.IsAdmin {
		q += ` WHERE c.everyone = 1 OR c.id IN (SELECT cg.collection_id FROM collection_groups cg
			JOIN user_groups ug ON ug.group_id = cg.group_id WHERE ug.user_id = ?)`
		args = append(args, u.ID)
	}
	q += ` ORDER BY c.name COLLATE NOCASE`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	out := []*collectionRow{}
	byID := map[int64]*collectionRow{}
	for rows.Next() {
		c := &collectionRow{GroupIDs: []int64{}}
		rows.Scan(&c.ID, &c.Name, &c.Description, &c.Everyone, &c.FontCount)
		out = append(out, c)
		byID[c.ID] = c
	}
	rows.Close()
	if u.IsAdmin {
		gr, err := s.db.Query(`SELECT collection_id, group_id FROM collection_groups`)
		if err == nil {
			for gr.Next() {
				var cid, gid int64
				gr.Scan(&cid, &gid)
				if c := byID[cid]; c != nil {
					c.GroupIDs = append(c.GroupIDs, gid)
				}
			}
			gr.Close()
		}
	}
	writeJSON(w, 200, out)
}

type collectionReq struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Everyone    *bool    `json:"everyone"`
	GroupIDs    *[]int64 `json:"group_ids"`
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req collectionReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		writeErr(w, 400, "name required")
		return
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	everyone := req.Everyone != nil && *req.Everyone
	res, err := s.db.Exec(`INSERT INTO collections (name, description, everyone, created_at) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(*req.Name), desc, boolInt(everyone), now())
	if err != nil {
		writeErr(w, 409, "a collection with that name already exists")
		return
	}
	id, _ := res.LastInsertId()
	if req.GroupIDs != nil {
		s.setCollectionGroups(id, *req.GroupIDs)
	}
	s.audit(u, "collection.create", "%s", *req.Name)
	writeJSON(w, 201, map[string]int64{"id": id})
}

func (s *Server) setCollectionGroups(id int64, groups []int64) {
	s.db.Exec(`DELETE FROM collection_groups WHERE collection_id = ?`, id)
	for _, g := range groups {
		s.db.Exec(`INSERT OR IGNORE INTO collection_groups (collection_id, group_id) SELECT ?, id FROM groups WHERE id = ?`, id, g)
	}
}

func (s *Server) handleUpdateCollection(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req collectionReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			writeErr(w, 400, "name required")
			return
		}
		if _, err := s.db.Exec(`UPDATE collections SET name = ? WHERE id = ?`, strings.TrimSpace(*req.Name), id); err != nil {
			writeErr(w, 409, "a collection with that name already exists")
			return
		}
	}
	if req.Description != nil {
		s.db.Exec(`UPDATE collections SET description = ? WHERE id = ?`, *req.Description, id)
	}
	if req.Everyone != nil {
		s.db.Exec(`UPDATE collections SET everyone = ? WHERE id = ?`, boolInt(*req.Everyone), id)
	}
	if req.GroupIDs != nil {
		s.setCollectionGroups(id, *req.GroupIDs)
	}
	s.pruneActivations()
	s.audit(u, "collection.update", "collection %d", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var name string
	s.db.QueryRow(`SELECT name FROM collections WHERE id = ?`, id).Scan(&name)
	s.db.Exec(`DELETE FROM collections WHERE id = ?`, id)
	s.pruneActivations()
	s.audit(u, "collection.delete", "%s", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCollectionAddFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		FontIDs []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	for _, f := range req.FontIDs {
		s.db.Exec(`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id)
			SELECT c.id, f.id FROM collections c, fonts f WHERE c.id = ? AND f.id = ?`, id, f)
	}
	s.audit(u, "collection.add", "%d font(s) → collection %d", len(req.FontIDs), id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCollectionRemoveFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		FontIDs []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.FontIDs) > 0 {
		args := append([]any{id}, int64Args(req.FontIDs)...)
		s.db.Exec(`DELETE FROM collection_fonts WHERE collection_id = ? AND font_id IN (`+placeholders(len(req.FontIDs))+`)`, args...)
	}
	s.pruneActivations()
	s.audit(u, "collection.remove", "%d font(s) ← collection %d", len(req.FontIDs), id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// pruneActivations removes activations for fonts a (non-admin) user can no
// longer see, so their agents deactivate them on the next sync.
func (s *Server) pruneActivations() {
	_, err := s.db.Exec(`DELETE FROM activations WHERE
		user_id NOT IN (SELECT id FROM users WHERE is_admin = 1)
		AND font_id NOT IN (
			SELECT cf.font_id FROM collection_fonts cf JOIN collections c ON c.id = cf.collection_id
			WHERE c.everyone = 1 OR c.id IN (
				SELECT cg.collection_id FROM collection_groups cg JOIN user_groups ug ON ug.group_id = cg.group_id
				WHERE ug.user_id = activations.user_id))`)
	if err != nil {
		s.log.Error("prune activations", "err", err)
	}
}
