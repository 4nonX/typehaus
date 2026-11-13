package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/4nonX/typehaus/internal/protocol"
)

// Endpoints for app plugins (InDesign, Photoshop, Illustrator, Affinity).
// Plugins talk to the typehaus app on the same computer; the app forwards
// with its device token, so everything happens for the person who uses that
// computer – with their visibility and the license rules.

const maxResolve = 500

// deviceUser is the person behind a device (not for all-users computers:
// plugins run in a person's session).
func (s *Server) deviceUser(w http.ResponseWriter, d *authDevice) *authUser {
	if d.Machine {
		writeErrCode(w, 400, "invalid_request", "plugins work with the typehaus app of a person, not the all-users service")
		return nil
	}
	u := &authUser{ID: d.UserID}
	if err := s.db.QueryRow(`SELECT username, display_name, is_admin FROM users WHERE id = ?`, d.UserID).
		Scan(&u.Username, &u.DisplayName, &u.IsAdmin); err != nil {
		writeErr(w, 401, "unknown user")
		return nil
	}
	return u
}

// handlePluginResolve: which of a document's fonts does this person have,
// and which can typehaus provide?
func (s *Server) handlePluginResolve(w http.ResponseWriter, r *http.Request, d *authDevice) {
	u := s.deviceUser(w, d)
	if u == nil {
		return
	}
	var req protocol.ResolveRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Fonts) > maxResolve {
		writeErr(w, 400, "too many fonts in one request")
		return
	}
	where, vargs := visibleClause(u)
	out := protocol.ResolveResponse{Fonts: []protocol.FontStatus{}}
	for _, ref := range req.Fonts {
		st := protocol.FontStatus{FontRef: ref, Status: "not_available"}
		var id int64
		var family, style, full, sha, platforms, kind string
		var active bool
		find := func(cond string, args ...any) bool {
			q := `SELECT f.id, f.family, f.style, f.full_name, f.sha256, f.platforms, f.kind,
				EXISTS(SELECT 1 FROM user_fonts a WHERE a.font_id = f.id AND a.user_id = ?)
				FROM fonts f WHERE ` + cond + ` AND ` + where + ` ORDER BY f.id DESC LIMIT 1` // newest version
			all := append(append([]any{u.ID}, args...), vargs...)
			return s.db.QueryRow(q, all...).Scan(&id, &family, &style, &full, &sha, &platforms, &kind, &active) == nil
		}
		ps := strings.TrimSpace(ref.PostScript)
		fam, sty := strings.TrimSpace(ref.Family), strings.TrimSpace(ref.Style)
		found := ps != "" && find(`f.postscript_name = ? COLLATE NOCASE`, ps)
		if !found && fam != "" {
			if sty == "" {
				sty = "Regular"
			}
			full := strings.TrimSpace(fam + " " + sty)
			// plain placeholders only: the visibility clause adds its own
			found = find(`(f.family = ? COLLATE NOCASE OR f.family_auto = ? COLLATE NOCASE OR f.full_name = ? COLLATE NOCASE)
				AND (f.style = ? COLLATE NOCASE OR f.style_auto = ? COLLATE NOCASE OR f.full_name = ? COLLATE NOCASE)`,
				fam, fam, full, sty, sty, full)
		}
		if found {
			st.FontID, st.Family, st.Style, st.FullName, st.SHA256 = id, family, style, full, sha
			switch {
			case platforms != "" && d.Platform != "" && !strings.Contains(","+platforms+",", ","+d.Platform+","):
				st.Status, st.Reason = "unsupported", "this font format can't be installed on "+platformName(d.Platform)
			case !agentSupportsKind(d.Agent, kind):
				st.Status, st.Reason = "unsupported", "update the typehaus app to install this font format"
			default:
				if blocked, why := s.activationRefusals(u.ID, []int64{id}); blocked[id] {
					st.Status, st.Reason = "refused", strings.Join(why, "; ")
				} else if active {
					st.Status = "active"
				} else {
					st.Status = "available"
				}
			}
		}
		out.Fonts = append(out.Fonts, st)
	}
	writeJSON(w, 200, out)
}

// handlePluginActivate switches fonts on for the device's person (same rules
// as the library: visibility, license seats, one version per PostScript name).
func (s *Server) handlePluginActivate(w http.ResponseWriter, r *http.Request, d *authDevice) {
	u := s.deviceUser(w, d)
	if u == nil {
		return
	}
	u.IP = s.clientIP(r)
	u.Username += " (plugin)"
	s.handleActivate(w, r, u)
}

// handlePluginRequest files a font request for the device's person.
func (s *Server) handlePluginRequest(w http.ResponseWriter, r *http.Request, d *authDevice) {
	u := s.deviceUser(w, d)
	if u == nil {
		return
	}
	u.IP = s.clientIP(r)
	// note where the request came from
	var req struct {
		Font    string `json:"font"`
		Purpose string `json:"purpose"`
		App     string `json:"app"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.App != "" {
		req.Purpose = strings.TrimSpace(req.Purpose + "\n(missing in a document in " + trunc(req.App, 60) + ")")
	}
	b, _ := json.Marshal(map[string]string{"font": req.Font, "purpose": req.Purpose})
	r.Body = io.NopCloser(bytes.NewReader(b))
	s.handleCreateRequest(w, r, u)
}

func platformName(p string) string {
	switch p {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return p
}
