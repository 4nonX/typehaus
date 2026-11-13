package server

import (
	"database/sql"
	"net/http"
	"strings"
)

// Font requests: anyone can ask for a font that isn't in the library yet;
// admins answer (added, or declined with a reason). Adding the font, its
// license and the collection that distributes it stays the admins' job.

type fontRequest struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	User       string `json:"user"`
	Font       string `json:"font"`
	Purpose    string `json:"purpose"`
	Status     string `json:"status"` // open | done | declined
	Answer     string `json:"answer"`
	Seen       bool   `json:"seen"`
	CreatedAt  int64  `json:"created_at"`
	ResolvedAt *int64 `json:"resolved_at,omitempty"`
	ResolvedBy string `json:"resolved_by,omitempty"`
}

const maxOpenRequests = 25

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request, u *authUser) {
	q := `SELECT fr.id, fr.user_id, COALESCE(NULLIF(u.display_name, ''), u.username), fr.font, fr.purpose, fr.status, fr.answer, fr.seen,
		fr.created_at, fr.resolved_at, COALESCE(NULLIF(rb.display_name, ''), rb.username, '')
		FROM font_requests fr JOIN users u ON u.id = fr.user_id LEFT JOIN users rb ON rb.id = fr.resolved_by`
	var args []any
	// admins see everyone's requests; ?mine=1 limits to their own
	if !u.IsAdmin || r.URL.Query().Get("mine") == "1" {
		q += ` WHERE fr.user_id = ?`
		args = append(args, u.ID)
	}
	q += ` ORDER BY fr.status = 'open' DESC, COALESCE(fr.resolved_at, fr.created_at) DESC LIMIT 500`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []fontRequest{}
	for rows.Next() {
		var x fontRequest
		var res sql.NullInt64
		rows.Scan(&x.ID, &x.UserID, &x.User, &x.Font, &x.Purpose, &x.Status, &x.Answer, &x.Seen, &x.CreatedAt, &res, &x.ResolvedBy)
		if res.Valid {
			x.ResolvedAt = &res.Int64
		}
		out = append(out, x)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		Font    string `json:"font"`
		Purpose string `json:"purpose"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	font := trunc(strings.TrimSpace(req.Font), 200)
	if font == "" {
		writeErr(w, 400, "which font? Enter its name")
		return
	}
	var open int
	s.db.QueryRow(`SELECT COUNT(*) FROM font_requests WHERE user_id = ? AND status = 'open'`, u.ID).Scan(&open)
	if open >= maxOpenRequests {
		writeErr(w, 429, "you have many open requests already – wait for an answer first")
		return
	}
	var dup bool
	s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM font_requests WHERE user_id = ? AND status = 'open' AND font = ? COLLATE NOCASE)`, u.ID, font).Scan(&dup)
	if dup {
		writeErr(w, 409, "you already requested “"+font+"”")
		return
	}
	res, err := s.db.Exec(`INSERT INTO font_requests (user_id, font, purpose, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, font, trunc(strings.TrimSpace(req.Purpose), 2000), now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(u, "request.create", "%s", font)
	writeJSON(w, 201, map[string]int64{"id": id})
}

// handleUpdateRequest: admins answer (status, answer); the requester marks
// answers as seen ({"seen": true}).
func (s *Server) handleUpdateRequest(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Status *string `json:"status"`
		Answer *string `json:"answer"`
		Seen   *bool   `json:"seen"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var owner int64
	var font string
	if err := s.db.QueryRow(`SELECT user_id, font FROM font_requests WHERE id = ?`, id).Scan(&owner, &font); err != nil {
		writeErr(w, 404, "request not found")
		return
	}
	if req.Seen != nil && owner == u.ID {
		s.db.Exec(`UPDATE font_requests SET seen = 1 WHERE id = ?`, id)
	}
	if req.Status == nil && req.Answer == nil {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !u.IsAdmin {
		writeErr(w, 403, "only admins answer requests")
		return
	}
	answer := ""
	if req.Answer != nil {
		answer = trunc(strings.TrimSpace(*req.Answer), 2000)
	}
	if req.Status != nil {
		st := *req.Status
		if st != "open" && st != "done" && st != "declined" {
			writeErr(w, 400, "status must be open, done or declined")
			return
		}
		if st == "open" {
			s.db.Exec(`UPDATE font_requests SET status = 'open', resolved_at = NULL, resolved_by = NULL, seen = 1 WHERE id = ?`, id)
		} else {
			// the requester sees the answer in their library
			s.db.Exec(`UPDATE font_requests SET status = ?, answer = ?, resolved_at = ?, resolved_by = ?, seen = ? WHERE id = ?`,
				st, answer, now(), u.ID, boolInt(owner == u.ID), id)
		}
		s.audit(u, "request."+st, "%s", font)
	} else {
		s.db.Exec(`UPDATE font_requests SET answer = ? WHERE id = ?`, answer, id)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleDeleteRequest: requesters withdraw open requests; admins delete any.
func (s *Server) handleDeleteRequest(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var owner int64
	var status, font string
	if err := s.db.QueryRow(`SELECT user_id, status, font FROM font_requests WHERE id = ?`, id).Scan(&owner, &status, &font); err != nil {
		writeErr(w, 404, "request not found")
		return
	}
	if !u.IsAdmin && (owner != u.ID || status != "open") {
		writeErr(w, 403, "only open requests of your own can be withdrawn")
		return
	}
	s.db.Exec(`DELETE FROM font_requests WHERE id = ?`, id)
	s.audit(u, "request.delete", "%s", font)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
