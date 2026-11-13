package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// Outside services typehaus can talk to: the free font catalogs and the font
// identification services. Keys an admin enters are kept in the settings
// table; a TYPEHAUS_* environment variable takes precedence over a stored key.

type serviceField struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
	Env    string `json:"env"`
}

type serviceDef struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Kind    string         `json:"kind"` // identify, catalog
	About   string         `json:"about"`
	Signup  string         `json:"signup,omitempty"`
	Privacy string         `json:"privacy,omitempty"`
	Fields  []serviceField `json:"fields"`
}

var services = []serviceDef{
	{ID: "whatfontis", Name: "WhatFontIs", Kind: "identify",
		About:   "Matches against more than a million free and commercial fonts. The API key is free for up to 200 lookups a day; ask WhatFontIs for more.",
		Signup:  "https://www.whatfontis.com/API-identify-fonts-from-image.html",
		Privacy: "The image is sent to WhatFontIs.",
		Fields:  []serviceField{{"key", "API key", true, "TYPEHAUS_WHATFONTIS_KEY"}}},
	{ID: "mixfont", Name: "Mixfont Lens", Kind: "identify",
		About:   "Finds the closest open-source font (most of Google Fonts and more), so a match can usually be imported from the free catalog. Paid API with credits.",
		Signup:  "https://www.mixfont.com/font-recognition-api",
		Privacy: "Mixfont fetches the image from a link: an image link you give, or a temporary link on this server, which then has to be reachable from the internet (TYPEHAUS_BASE_URL).",
		Fields:  []serviceField{{"key", "API key", true, "TYPEHAUS_MIXFONT_KEY"}}},
	{ID: "aspose", Name: "Aspose.OCR Cloud", Kind: "identify",
		About:   "Names the fonts and style used in a scan or photo, mostly common system fonts. Needs a Client ID and Client Secret from the Aspose Cloud dashboard.",
		Signup:  "https://dashboard.aspose.cloud/",
		Privacy: "The image is sent to Aspose.",
		Fields: []serviceField{{"client_id", "Client ID", false, "TYPEHAUS_ASPOSE_CLIENT_ID"},
			{"client_secret", "Client Secret", true, "TYPEHAUS_ASPOSE_CLIENT_SECRET"}}},
}

func serviceByID(id string) *serviceDef {
	for i := range services {
		if services[i].ID == id {
			return &services[i]
		}
	}
	return nil
}

// serviceValue returns a service setting and whether it comes from the
// environment.
func (s *Server) serviceValue(id, field string) (string, bool) {
	d := serviceByID(id)
	if d == nil {
		return "", false
	}
	for _, f := range d.Fields {
		if f.Name != field {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(f.Env)); v != "" {
			return v, true
		}
	}
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, "service."+id+"."+field).Scan(&v)
	return v, false
}

// serviceConfigured reports whether every field of a service has a value.
func (s *Server) serviceConfigured(id string) bool {
	d := serviceByID(id)
	if d == nil {
		return false
	}
	for _, f := range d.Fields {
		if v, _ := s.serviceValue(id, f.Name); v == "" {
			return false
		}
	}
	return true
}

type serviceFieldState struct {
	serviceField
	Set     bool   `json:"set"`
	FromEnv bool   `json:"from_env"`
	Hint    string `json:"hint,omitempty"` // last characters of a secret, or a plain value
}

type serviceState struct {
	serviceDef
	Fields     []serviceFieldState `json:"fields"`
	Configured bool                `json:"configured"`
	Enabled    bool                `json:"enabled"`
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request, u *authUser) {
	out := []serviceState{}
	for _, d := range services {
		st := serviceState{serviceDef: d, Configured: s.serviceConfigured(d.ID), Enabled: s.serviceEnabled(d)}
		for _, f := range d.Fields {
			v, env := s.serviceValue(d.ID, f.Name)
			fs := serviceFieldState{serviceField: f, Set: v != "", FromEnv: env}
			if v != "" {
				if f.Secret {
					if len(v) > 8 {
						fs.Hint = "…" + v[len(v)-4:]
					}
				} else {
					fs.Hint = v
				}
			}
			st.Fields = append(st.Fields, fs)
		}
		out = append(out, st)
	}
	writeJSON(w, 200, out)
}

func (s *Server) serviceEnabled(d serviceDef) bool {
	if d.Kind == "identify" {
		return s.cfg.Identify
	}
	return s.cfg.Catalog
}

// handleSetService stores the fields given; an empty value removes one.
func (s *Server) handleSetService(w http.ResponseWriter, r *http.Request, u *authUser) {
	d := serviceByID(r.PathValue("id"))
	if d == nil {
		writeErr(w, 404, "unknown service")
		return
	}
	var req map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	var changed []string
	for _, f := range d.Fields {
		v, ok := req[f.Name]
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) > 500 {
			writeErr(w, 400, f.Label+" is too long")
			return
		}
		key := "service." + d.ID + "." + f.Name
		if v == "" {
			s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
		} else if _, err := s.db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, v, now()); err != nil {
			s.internalErr(w, err)
			return
		}
		changed = append(changed, f.Label)
	}
	if len(changed) > 0 {
		s.audit(u, "service.update", "%s: %s", d.Name, strings.Join(changed, ", "))
	}
	w.WriteHeader(204)
}
