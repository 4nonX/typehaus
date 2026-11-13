package server

import (
	"fmt"
	"net/http"
	"strings"
)

type collectionRow struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Everyone    bool    `json:"everyone"`
	Mode        string  `json:"mode"` // available | installed
	GroupIDs    []int64 `json:"group_ids"`
	FontCount   int     `json:"font_count"`
	Members     int     `json:"members"` // people the collection applies to
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request, u *authUser) {
	q := `SELECT c.id, c.name, c.description, c.everyone, c.mode,
		(SELECT COUNT(*) FROM collection_fonts cf WHERE cf.collection_id = c.id),
		(SELECT COUNT(*) FROM users u WHERE u.disabled = 0 AND (c.everyone = 1 OR EXISTS (
			SELECT 1 FROM collection_groups cg JOIN user_groups ug ON ug.group_id = cg.group_id
			WHERE cg.collection_id = c.id AND ug.user_id = u.id)))
		FROM collections c`
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
		rows.Scan(&c.ID, &c.Name, &c.Description, &c.Everyone, &c.Mode, &c.FontCount, &c.Members)
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
	Mode        *string  `json:"mode"`
	GroupIDs    *[]int64 `json:"group_ids"`
}

func validMode(m string) bool { return m == "available" || m == "installed" }

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
	mode := "available"
	if req.Mode != nil {
		if !validMode(*req.Mode) {
			writeErr(w, 400, "mode must be available or installed")
			return
		}
		mode = *req.Mode
	}
	// a new collection has no fonts yet: nothing to check
	res, err := s.db.Exec(`INSERT INTO collections (name, description, everyone, mode, created_at) VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(*req.Name), desc, boolInt(everyone), mode, now())
	if err != nil {
		writeErrCode(w, 409, "already_exists", "a collection with that name already exists")
		return
	}
	id, _ := res.LastInsertId()
	if req.GroupIDs != nil {
		s.setCollectionGroups(id, *req.GroupIDs)
	}
	s.audit(u, "collection.create", "%s", *req.Name)
	s.emit("collection.created", map[string]any{"collection": s.eventCollection(id), "by": u.Username})
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
	if req.Mode != nil && !validMode(*req.Mode) {
		writeErr(w, 400, "mode must be available or installed")
		return
	}
	// Installing automatically must stay within enforced licenses.
	cur, ok := s.collectionAudience(id)
	if !ok {
		writeErr(w, 404, "collection not found")
		return
	}
	next := cur
	if req.Mode != nil {
		next.Mode = *req.Mode
	}
	if req.Everyone != nil {
		next.Everyone = *req.Everyone
	}
	if req.GroupIDs != nil {
		next.GroupIDs = *req.GroupIDs
	}
	if msg := s.blockedBySeats(next, nil); msg != "" {
		writeErrCode(w, 409, "seats_exhausted", msg)
		return
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			writeErr(w, 400, "name required")
			return
		}
		if _, err := s.db.Exec(`UPDATE collections SET name = ? WHERE id = ?`, strings.TrimSpace(*req.Name), id); err != nil {
			writeErrCode(w, 409, "already_exists", "a collection with that name already exists")
			return
		}
	}
	if req.Description != nil {
		s.db.Exec(`UPDATE collections SET description = ? WHERE id = ?`, *req.Description, id)
	}
	if req.Everyone != nil {
		s.db.Exec(`UPDATE collections SET everyone = ? WHERE id = ?`, boolInt(*req.Everyone), id)
	}
	if req.Mode != nil && *req.Mode != cur.Mode {
		s.db.Exec(`UPDATE collections SET mode = ? WHERE id = ?`, *req.Mode, id)
		s.audit(u, "collection.mode", "%s: %s", cur.Name, *req.Mode)
	}
	if req.GroupIDs != nil {
		s.setCollectionGroups(id, *req.GroupIDs)
	}
	s.pruneActivations()
	s.audit(u, "collection.update", "collection %d", id)
	s.usageChanged()
	s.emit("collection.updated", map[string]any{"collection": s.eventCollection(id), "by": u.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("collections", id) {
		writeErr(w, 404, "collection not found")
		return
	}
	var name string
	s.db.QueryRow(`SELECT name FROM collections WHERE id = ?`, id).Scan(&name)
	s.db.Exec(`DELETE FROM collections WHERE id = ?`, id)
	s.pruneActivations()
	s.audit(u, "collection.delete", "%s", name)
	s.usageChanged()
	s.emit("collection.deleted", map[string]any{"collection": map[string]any{"id": id, "name": name}, "by": u.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCollectionAddFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("collections", id) {
		writeErr(w, 404, "collection not found")
		return
	}
	var req struct {
		FontIDs []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if cur, ok := s.collectionAudience(id); ok {
		if msg := s.blockedBySeats(cur, req.FontIDs); msg != "" {
			writeErrCode(w, 409, "seats_exhausted", msg)
			return
		}
	}
	for _, f := range req.FontIDs {
		s.db.Exec(`INSERT OR IGNORE INTO collection_fonts (collection_id, font_id)
			SELECT c.id, f.id FROM collections c, fonts f WHERE c.id = ? AND f.id = ?`, id, f)
	}
	s.audit(u, "collection.add", "%d font(s) → collection %d", len(req.FontIDs), id)
	s.usageChanged()
	s.emit("collection.updated", map[string]any{"collection": s.eventCollection(id), "added_fonts": s.eventFonts(req.FontIDs), "by": u.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCollectionRemoveFonts(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("collections", id) {
		writeErr(w, 404, "collection not found")
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
	s.usageChanged()
	s.emit("collection.updated", map[string]any{"collection": s.eventCollection(id), "removed_fonts": s.eventFonts(req.FontIDs), "by": u.Username})
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

// ---- automatic installation and seats ---------------------------------------------

type audience struct {
	ID       int64
	Name     string
	Mode     string
	Everyone bool
	GroupIDs []int64
}

func (s *Server) collectionAudience(id int64) (audience, bool) {
	a := audience{ID: id, GroupIDs: []int64{}}
	if err := s.db.QueryRow(`SELECT name, mode, everyone FROM collections WHERE id = ?`, id).Scan(&a.Name, &a.Mode, &a.Everyone); err != nil {
		return a, false
	}
	if rows, err := s.db.Query(`SELECT group_id FROM collection_groups WHERE collection_id = ?`, id); err == nil {
		for rows.Next() {
			var g int64
			rows.Scan(&g)
			a.GroupIDs = append(a.GroupIDs, g)
		}
		rows.Close()
	}
	return a, true
}

// members returns the active users a collection would apply to.
func (s *Server) members(a audience) map[int64]bool {
	out := map[int64]bool{}
	q := `SELECT id FROM users WHERE disabled = 0`
	var args []any
	if !a.Everyone {
		if len(a.GroupIDs) == 0 {
			return out
		}
		q += ` AND id IN (SELECT user_id FROM user_groups WHERE group_id IN (` + placeholders(len(a.GroupIDs)) + `))`
		args = int64Args(a.GroupIDs)
	}
	if rows, err := s.db.Query(q, args...); err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out[id] = true
		}
		rows.Close()
	}
	return out
}

type seatImpact struct {
	LicenseID int64  `json:"license_id"`
	Name      string `json:"name"`
	Seats     int    `json:"seats"`
	Unit      string `json:"unit"`
	Enforce   bool   `json:"enforce"`
	Now       int    `json:"now"`   // users or devices using it today
	After     int    `json:"after"` // with the collection installed automatically
	Over      bool   `json:"over"`
}

type impactResult struct {
	Members    int          `json:"members"`
	Devices    int          `json:"devices"`
	Fonts      int          `json:"fonts"`
	Unlicensed int          `json:"unlicensed_families"`
	Licenses   []seatImpact `json:"licenses"`
}

// impact works out what installing a collection automatically means for
// the seats of every license involved; extraFonts are fonts about to be
// added.
func (s *Server) impact(a audience, extraFonts []int64) impactResult {
	res := impactResult{Licenses: []seatImpact{}}
	members := s.members(a)
	res.Members = len(members)
	fontIDs := map[int64]bool{}
	if rows, err := s.db.Query(`SELECT font_id FROM collection_fonts WHERE collection_id = ?`, a.ID); err == nil {
		for rows.Next() {
			var f int64
			rows.Scan(&f)
			fontIDs[f] = true
		}
		rows.Close()
	}
	for _, f := range extraFonts {
		fontIDs[f] = true
	}
	res.Fonts = len(fontIDs)
	if len(fontIDs) == 0 {
		return res
	}
	ids := make([]int64, 0, len(fontIDs))
	for f := range fontIDs {
		ids = append(ids, f)
	}
	ph := placeholders(len(ids))
	s.db.QueryRow(`SELECT COUNT(DISTINCT family) FROM fonts WHERE license_id IS NULL AND id IN (`+ph+`)`, int64Args(ids)...).Scan(&res.Unlicensed)
	// devices of the members
	memberDevices := map[int64]bool{}
	if len(members) > 0 {
		mids := make([]int64, 0, len(members))
		for m := range members {
			mids = append(mids, m)
		}
		if rows, err := s.db.Query(`SELECT id FROM devices WHERE user_id IN (`+placeholders(len(mids))+`)`, int64Args(mids)...); err == nil {
			for rows.Next() {
				var d int64
				rows.Scan(&d)
				memberDevices[d] = true
			}
			rows.Close()
		}
	}
	res.Devices = len(memberDevices)
	// administrators and their own computers (not all-users ones): left out
	// of licenses that don't count admins
	admins, adminDevices := map[int64]bool{}, map[int64]bool{}
	if r, err := s.db.Query(`SELECT id FROM users WHERE is_admin = 1`); err == nil {
		for r.Next() {
			var u int64
			r.Scan(&u)
			admins[u] = true
		}
		r.Close()
	}
	if r, err := s.db.Query(`SELECT d.id FROM devices d JOIN users u ON u.id = d.user_id WHERE u.is_admin = 1 AND d.platform NOT LIKE '%-machine'`); err == nil {
		for r.Next() {
			var d int64
			r.Scan(&d)
			adminDevices[d] = true
		}
		r.Close()
	}
	rows, err := s.db.Query(`SELECT DISTINCT l.id, l.name, l.seats, l.seat_unit, l.enforce, l.count_admins FROM licenses l
		JOIN fonts f ON f.license_id = l.id WHERE f.id IN (`+ph+`) ORDER BY l.name COLLATE NOCASE`, int64Args(ids)...)
	if err != nil {
		return res
	}
	type licRow struct {
		seatImpact
		countAdmins bool
	}
	var lics []licRow
	for rows.Next() {
		var l licRow
		rows.Scan(&l.LicenseID, &l.Name, &l.Seats, &l.Unit, &l.Enforce, &l.countAdmins)
		lics = append(lics, l)
	}
	rows.Close()
	for _, lr := range lics {
		l := lr.seatImpact
		skipUser := func(u int64) bool { return !lr.countAdmins && admins[u] }
		skipDev := func(d int64) bool { return !lr.countAdmins && adminDevices[d] }
		users := map[int64]bool{}
		devs := map[int64]bool{}
		if r2, err := s.db.Query(`SELECT DISTINCT a.user_id FROM user_fonts a JOIN fonts f ON f.id = a.font_id WHERE f.license_id = ?`, l.LicenseID); err == nil {
			for r2.Next() {
				var u int64
				r2.Scan(&u)
				if !skipUser(u) {
					users[u] = true
				}
			}
			r2.Close()
		}
		if r2, err := s.db.Query(`SELECT DISTINCT df.device_id FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256 WHERE f.license_id = ?`, l.LicenseID); err == nil {
			for r2.Next() {
				var d int64
				r2.Scan(&d)
				if !skipDev(d) {
					devs[d] = true
				}
			}
			r2.Close()
		}
		if l.Unit == "devices" {
			l.Now = len(devs)
		} else {
			l.Now = len(users)
		}
		if a.Mode == "installed" {
			for m := range members {
				if !skipUser(m) {
					users[m] = true
				}
			}
			for d := range memberDevices {
				if !skipDev(d) {
					devs[d] = true
				}
			}
		}
		if l.Unit == "devices" {
			l.After = len(devs)
		} else {
			l.After = len(users)
		}
		l.Over = l.Seats > 0 && l.After > l.Seats
		res.Licenses = append(res.Licenses, l)
	}
	return res
}

// blockedBySeats explains why a collection can't be installed
// automatically: an enforced license would need more seats than it has.
func (s *Server) blockedBySeats(a audience, extraFonts []int64) string {
	if a.Mode != "installed" {
		return ""
	}
	var why []string
	for _, l := range s.impact(a, extraFonts).Licenses {
		if l.Enforce && l.Over {
			why = append(why, fmt.Sprintf("“%s” would need %d %s but has %d seats", l.Name, l.After, l.Unit, l.Seats))
		}
	}
	if len(why) == 0 {
		return ""
	}
	return "Can't install automatically – not enough licenses: " + strings.Join(why, "; ") +
		". Buy more seats, narrow the groups, or remove those fonts from the collection."
}

// handleCollectionImpact previews the seat impact of a change before it is
// saved: POST /api/collections/impact {id?, mode, everyone, group_ids, font_ids?}
func (s *Server) handleCollectionImpact(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		ID       int64   `json:"id"`
		Mode     string  `json:"mode"`
		Everyone bool    `json:"everyone"`
		GroupIDs []int64 `json:"group_ids"`
		FontIDs  []int64 `json:"font_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validMode(req.Mode) {
		writeErr(w, 400, "mode must be available or installed")
		return
	}
	a := audience{ID: req.ID, Mode: req.Mode, Everyone: req.Everyone, GroupIDs: req.GroupIDs}
	writeJSON(w, 200, s.impact(a, req.FontIDs))
}
