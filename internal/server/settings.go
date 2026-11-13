package server

import "net/http"

// Server-wide settings administrators change in the web UI.

// existingFontsPolicy: what the typehaus app does with a library font whose
// PostScript name is already installed on the computer outside typehaus.
// "skip" (default): no second copy – the computer reports it; "install":
// install anyway.
func (s *Server) existingFontsPolicy() string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = 'existing_fonts'`).Scan(&v)
	if v != "install" {
		return "skip"
	}
	return v
}

type settingsBody struct {
	ExistingFonts string `json:"existing_fonts"` // skip | install
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, u *authUser) {
	writeJSON(w, 200, settingsBody{ExistingFonts: s.existingFontsPolicy()})
}

func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		ExistingFonts *string `json:"existing_fonts"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.ExistingFonts != nil {
		v := *req.ExistingFonts
		if v != "skip" && v != "install" {
			writeErr(w, 400, "existing_fonts must be skip or install")
			return
		}
		if _, err := s.db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('existing_fonts', ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, v, now()); err != nil {
			s.internalErr(w, err)
			return
		}
		s.audit(u, "settings.update", "fonts already on a computer: %s", v)
	}
	writeJSON(w, 200, settingsBody{ExistingFonts: s.existingFontsPolicy()})
}
