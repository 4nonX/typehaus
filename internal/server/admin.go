package server

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

// ---- users ----------------------------------------------------------------------

type userRow struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Email       string  `json:"email"`
	IsAdmin     bool    `json:"is_admin"`
	Disabled    bool    `json:"disabled"`
	SSO         bool    `json:"sso"`
	HasPassword bool    `json:"has_password"`
	GroupIDs    []int64 `json:"group_ids"`
	SSOGroupIDs []int64 `json:"sso_group_ids"` // memberships managed by SSO group sync
	Devices     int     `json:"devices"`
	Active      int     `json:"active_fonts"`
	CreatedAt   int64   `json:"created_at"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT u.id, u.username, u.display_name, u.email, u.is_admin, u.disabled,
		u.oidc_subject IS NOT NULL, COALESCE(u.password_hash, '') != '',
		(SELECT COUNT(*) FROM devices d WHERE d.user_id = u.id),
		(SELECT COUNT(*) FROM user_fonts a WHERE a.user_id = u.id), u.created_at
		FROM users u ORDER BY u.username COLLATE NOCASE`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	out := []*userRow{}
	byID := map[int64]*userRow{}
	for rows.Next() {
		x := &userRow{GroupIDs: []int64{}, SSOGroupIDs: []int64{}}
		rows.Scan(&x.ID, &x.Username, &x.DisplayName, &x.Email, &x.IsAdmin, &x.Disabled, &x.SSO, &x.HasPassword,
			&x.Devices, &x.Active, &x.CreatedAt)
		out = append(out, x)
		byID[x.ID] = x
	}
	rows.Close()
	gr, err := s.db.Query(`SELECT user_id, group_id, source FROM user_groups`)
	if err == nil {
		for gr.Next() {
			var uid, gid int64
			var src string
			gr.Scan(&uid, &gid, &src)
			if x := byID[uid]; x != nil {
				x.GroupIDs = append(x.GroupIDs, gid)
				if src == "oidc" {
					x.SSOGroupIDs = append(x.SSOGroupIDs, gid)
				}
			}
		}
		gr.Close()
	}
	writeJSON(w, 200, out)
}

type userReq struct {
	Username    *string  `json:"username"`
	DisplayName *string  `json:"display_name"`
	Email       *string  `json:"email"`
	Password    *string  `json:"password"`
	IsAdmin     *bool    `json:"is_admin"`
	Disabled    *bool    `json:"disabled"`
	GroupIDs    *[]int64 `json:"group_ids"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req userReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Username == nil || strings.TrimSpace(*req.Username) == "" {
		writeErr(w, 400, "username required")
		return
	}
	var hash any
	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < 10 {
			writeErr(w, 400, "password must be at least 10 characters")
			return
		}
		h, err := hashPassword(*req.Password)
		if err != nil {
			s.internalErr(w, err)
			return
		}
		hash = h
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	res, err := s.db.Exec(`INSERT INTO users (username, display_name, email, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		str(req.Username), str(req.DisplayName), str(req.Email), hash, boolInt(req.IsAdmin != nil && *req.IsAdmin), now())
	if err != nil {
		writeErrCode(w, 409, "already_exists", "username already exists")
		return
	}
	id, _ := res.LastInsertId()
	if req.GroupIDs != nil {
		s.setUserGroups(id, *req.GroupIDs)
	}
	s.audit(u, "user.create", "%s", str(req.Username))
	s.emit("user.created", map[string]any{"user": s.eventUser(id), "by": u.Username})
	writeJSON(w, 201, map[string]int64{"id": id})
}

// setUserGroups sets the manual memberships. SSO-managed memberships are
// kept unless the admin unticks them (they come back at the next SSO login
// if the identity provider still lists the group).
func (s *Server) setUserGroups(id int64, groups []int64) {
	keep := map[int64]bool{}
	for _, g := range groups {
		keep[g] = true
	}
	rows, err := s.db.Query(`SELECT group_id, source FROM user_groups WHERE user_id = ?`, id)
	if err != nil {
		return
	}
	type m struct {
		gid int64
		src string
	}
	var cur []m
	for rows.Next() {
		var x m
		rows.Scan(&x.gid, &x.src)
		cur = append(cur, x)
	}
	rows.Close()
	have := map[int64]bool{}
	for _, x := range cur {
		if !keep[x.gid] {
			s.db.Exec(`DELETE FROM user_groups WHERE user_id = ? AND group_id = ?`, id, x.gid)
		}
		have[x.gid] = true
	}
	for _, g := range groups {
		if !have[g] {
			s.db.Exec(`INSERT OR IGNORE INTO user_groups (user_id, group_id, source) SELECT ?, id, 'manual' FROM groups WHERE id = ?`, id, g)
		}
	}
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("users", id) {
		writeErr(w, 404, "user not found")
		return
	}
	var req userReq
	if !readJSON(w, r, &req) {
		return
	}
	if id == u.ID && ((req.IsAdmin != nil && !*req.IsAdmin) || (req.Disabled != nil && *req.Disabled)) {
		writeErr(w, 400, "you cannot remove your own admin rights or disable yourself")
		return
	}
	if req.Username != nil {
		if strings.TrimSpace(*req.Username) == "" {
			writeErr(w, 400, "username required")
			return
		}
		if _, err := s.db.Exec(`UPDATE users SET username = ? WHERE id = ?`, strings.TrimSpace(*req.Username), id); err != nil {
			writeErrCode(w, 409, "already_exists", "username already exists")
			return
		}
	}
	if req.DisplayName != nil {
		s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, *req.DisplayName, id)
	}
	if req.Email != nil {
		s.db.Exec(`UPDATE users SET email = ? WHERE id = ?`, *req.Email, id)
	}
	if req.Password != nil {
		if *req.Password == "" {
			s.db.Exec(`UPDATE users SET password_hash = NULL WHERE id = ?`, id)
		} else {
			if len(*req.Password) < 10 {
				writeErr(w, 400, "password must be at least 10 characters")
				return
			}
			h, err := hashPassword(*req.Password)
			if err != nil {
				s.internalErr(w, err)
				return
			}
			s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, h, id)
			s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
		}
	}
	if req.IsAdmin != nil {
		s.db.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, boolInt(*req.IsAdmin), id)
	}
	if req.Disabled != nil {
		s.db.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, boolInt(*req.Disabled), id)
		if *req.Disabled {
			s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
		}
	}
	if req.GroupIDs != nil {
		s.setUserGroups(id, *req.GroupIDs)
	}
	s.pruneActivations()
	s.audit(u, "user.update", "user %d", id)
	s.usageChanged()
	s.emit("user.updated", map[string]any{"user": s.eventUser(id), "by": u.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("users", id) {
		writeErr(w, 404, "user not found")
		return
	}
	if id == u.ID {
		writeErr(w, 400, "you cannot delete yourself")
		return
	}
	var name string
	s.db.QueryRow(`SELECT username FROM users WHERE id = ?`, id).Scan(&name)
	s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	s.audit(u, "user.delete", "%s", name)
	s.emit("user.deleted", map[string]any{"user": map[string]any{"id": id, "username": name}, "by": u.Username})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- groups ---------------------------------------------------------------------

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT g.id, g.name, g.description, (SELECT COUNT(*) FROM user_groups ug WHERE ug.group_id = g.id)
		FROM groups g ORDER BY g.name COLLATE NOCASE`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []groupRow{}
	for rows.Next() {
		var g groupRow
		rows.Scan(&g.ID, &g.Name, &g.Description, &g.Members)
		out = append(out, g)
	}
	writeJSON(w, 200, out)
}

type groupRow struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Members     int    `json:"members"`
}

// usageRow is one family in the usage report.
type usageRow struct {
	Family    string  `json:"family"`
	Faces     int     `json:"faces"`
	License   *string `json:"license"`
	Seats     *int    `json:"seats"`
	Users     int     `json:"users"`
	Admins    int     `json:"uncounted_admins"` // admins with it active whose license does not count them
	Devices   int     `json:"devices"`
	UserNames string  `json:"user_names"`
}

type auditEntry struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	Actor   string `json:"actor"` // username, token:<name>, or system
	Action  string `json:"action"`
	Detail  string `json:"detail"`
	IP      string `json:"ip"`
	TokenID *int64 `json:"token_id"` // set for changes made through an API token
}

type groupReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req groupReq
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
	res, err := s.db.Exec(`INSERT INTO groups (name, description) VALUES (?, ?)`, strings.TrimSpace(*req.Name), desc)
	if err != nil {
		writeErrCode(w, 409, "already_exists", "group already exists")
		return
	}
	id, _ := res.LastInsertId()
	s.audit(u, "group.create", "%s", *req.Name)
	writeJSON(w, 201, map[string]int64{"id": id})
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("groups", id) {
		writeErr(w, 404, "group not found")
		return
	}
	var req groupReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		if _, err := s.db.Exec(`UPDATE groups SET name = ? WHERE id = ?`, strings.TrimSpace(*req.Name), id); err != nil {
			writeErrCode(w, 409, "already_exists", "group already exists")
			return
		}
	}
	if req.Description != nil {
		s.db.Exec(`UPDATE groups SET description = ? WHERE id = ?`, *req.Description, id)
	}
	s.audit(u, "group.update", "group %d", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.exists("groups", id) {
		writeErr(w, 404, "group not found")
		return
	}
	var name string
	s.db.QueryRow(`SELECT name FROM groups WHERE id = ?`, id).Scan(&name)
	s.db.Exec(`DELETE FROM groups WHERE id = ?`, id)
	s.pruneActivations()
	s.audit(u, "group.delete", "%s", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- reports ----------------------------------------------------------------------

func (s *Server) handleUsageReport(w http.ResponseWriter, r *http.Request, u *authUser) {
	// Users and devices are counted the way each font's license counts them:
	// admins (and their own computers) are left out where a license says so.
	rows, err := s.db.Query(`SELECT f.family, COUNT(DISTINCT f.id), MAX(l.name), MAX(l.seats),
		(SELECT COUNT(DISTINCT a.user_id) FROM user_fonts a JOIN fonts f2 ON f2.id = a.font_id JOIN users au ON au.id = a.user_id
			LEFT JOIN licenses l2 ON l2.id = f2.license_id
			WHERE f2.family = f.family AND (l2.id IS NULL OR l2.count_admins = 1 OR au.is_admin = 0)),
		(SELECT COUNT(DISTINCT a.user_id) FROM user_fonts a JOIN fonts f2 ON f2.id = a.font_id JOIN users au ON au.id = a.user_id
			JOIN licenses l2 ON l2.id = f2.license_id
			WHERE f2.family = f.family AND l2.count_admins = 0 AND au.is_admin = 1
			AND NOT EXISTS (SELECT 1 FROM user_fonts a3 JOIN fonts f5 ON f5.id = a3.font_id LEFT JOIN licenses l3 ON l3.id = f5.license_id
				WHERE a3.user_id = a.user_id AND f5.family = f.family AND (l3.id IS NULL OR l3.count_admins = 1))),
		(SELECT COUNT(DISTINCT df.device_id) FROM device_fonts df JOIN fonts f3 ON f3.sha256 = df.sha256
			JOIN devices dd ON dd.id = df.device_id JOIN users du ON du.id = dd.user_id LEFT JOIN licenses l4 ON l4.id = f3.license_id
			WHERE f3.family = f.family AND (l4.id IS NULL OR l4.count_admins = 1 OR du.is_admin = 0 OR dd.platform LIKE '%-machine')),
		COALESCE((SELECT GROUP_CONCAT(DISTINCT u.username) FROM user_fonts a JOIN fonts f4 ON f4.id = a.font_id
			JOIN users u ON u.id = a.user_id WHERE f4.family = f.family), '')
		FROM fonts f LEFT JOIN licenses l ON l.id = f.license_id
		GROUP BY f.family ORDER BY f.family COLLATE NOCASE`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []usageRow{}
	for rows.Next() {
		var f usageRow
		var lic sql.NullString
		var seats sql.NullInt64
		rows.Scan(&f.Family, &f.Faces, &lic, &seats, &f.Users, &f.Admins, &f.Devices, &f.UserNames)
		if lic.Valid {
			f.License = &lic.String
			n := int(seats.Int64)
			f.Seats = &n
		}
		out = append(out, f)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, u *authUser) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	q := `SELECT id, ts, actor, action, detail, ip, token_id FROM audit`
	var args []any
	if before > 0 {
		q += ` WHERE id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []auditEntry{}
	for rows.Next() {
		var e auditEntry
		rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Detail, &e.IP, &e.TokenID)
		out = append(out, e)
	}
	if len(out) == limit { // more to come: cursor to the older entries
		v := r.URL.Query()
		v.Set("before", strconv.FormatInt(out[len(out)-1].ID, 10))
		v.Set("limit", strconv.Itoa(limit))
		w.Header().Set("Link", "<"+r.URL.Path+"?"+v.Encode()+">; rel=\"next\"")
	}
	writeJSON(w, 200, out)
}
