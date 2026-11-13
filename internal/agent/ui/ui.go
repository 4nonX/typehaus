// Package ui serves the agent window: a small local web page (127.0.0.1
// only, token protected) to connect this computer, see sync status and the
// installed fonts, and change settings. The tray opens it in the browser.
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/agent"
)

//go:embed web
var webFS embed.FS

const cookieName = "typehaus_agent"

// Server is the agent window backend.
type Server struct {
	c     *agent.Controller
	token string
	addr  string
	srv   *http.Server

	// OnQuit is called when another process (a newer version, or the CLI)
	// asks this instance to quit. Set by the tray / main loop.
	OnQuit func()
}

// Start listens on a random loopback port.
func Start(c *agent.Controller) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	rand.Read(b)
	s := &Server{c: c, token: hex.EncodeToString(b), addr: ln.Addr().String()}
	s.srv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return s, nil
}

// URL returns the address including the one-time login token.
func (s *Server) URL() string { return "http://" + s.addr + "/?t=" + s.token }

// Close shuts the server down.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.srv.Shutdown(ctx)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
				http.Error(w, "invalid link – open the window from the typehaus menu", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !s.authed(r) {
			http.Error(w, "Open this window from the typehaus menu.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
	mux.Handle("GET /app.js", files)
	mux.Handle("GET /app.css", files)
	mux.Handle("GET /icon.svg", files)

	mux.HandleFunc("GET /api/state", s.api(s.state))
	mux.HandleFunc("POST /api/enroll", s.api(s.enroll))
	mux.HandleFunc("POST /api/sync", s.api(func(w http.ResponseWriter, r *http.Request) {
		s.c.SyncNow()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/disconnect", s.api(func(w http.ResponseWriter, r *http.Request) {
		if err := s.c.Disconnect(); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/autostart", s.api(s.autostart))
	mux.HandleFunc("POST /api/quit", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
		if s.OnQuit != nil {
			go func() {
				time.Sleep(100 * time.Millisecond)
				s.OnQuit()
			}()
		}
	}))
	mux.HandleFunc("POST /api/open-library", s.api(func(w http.ResponseWriter, r *http.Request) {
		if st := s.c.Status(); st.Server != "" {
			agent.OpenURL(st.Server)
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/affinity/mode", s.api(s.affinityMode))
	mux.HandleFunc("POST /api/affinity/activate", s.api(s.affinityActivate))
	mux.HandleFunc("POST /api/affinity/request", s.api(s.affinityRequest))
	mux.HandleFunc("POST /api/affinity/check", s.api(func(w http.ResponseWriter, r *http.Request) {
		s.c.Affinity().Check()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /font/{sha}", s.font)

	return s.guard(mux)
}

// guard blocks DNS-rebinding (Host must be our loopback address) and sets
// strict headers.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; font-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(r *http.Request) bool {
	if h := r.Header.Get("X-Typehaus-Token"); h != "" { // local CLI / other instance
		return subtle.ConstantTimeCompare([]byte(h), []byte(s.token)) == 1
	}
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

func (s *Server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeErr(w, 403, "not authorised")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Typehaus") != "1" {
			writeErr(w, 403, "missing header")
			return
		}
		h(w, r)
	}
}

type fontInfo struct {
	SHA256   string `json:"sha256"`
	Family   string `json:"family"`
	Style    string `json:"style"`
	FullName string `json:"full_name"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	fonts := []fontInfo{}
	for _, f := range s.c.Installed() {
		fonts = append(fonts, fontInfo{SHA256: f.SHA256, Family: f.Family, Style: f.Style, FullName: f.FullName})
	}
	writeJSON(w, 200, map[string]any{
		"status":      s.c.Status(),
		"events":      s.c.Events(),
		"fonts":       fonts,
		"autostart":   agent.AutostartEnabled(),
		"version":     agent.Version,
		"hostname":    host,
		"os":          runtime.GOOS,
		"last_server": agent.LastServer(),
		"notice":      agent.InstallNotice(),
		"update":      s.c.Update(),
		"affinity":    s.c.Affinity().State(),
	})
}

func (s *Server) affinityMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.c.Affinity().SetMode(agent.AffinityMode(req.Mode)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) affinityActivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fonts []string `json:"fonts"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n, err := s.c.Affinity().ActivateMissing(r.Context(), req.Fonts)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"activated": n})
}

func (s *Server) affinityRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Font     string `json:"font"`
		Document string `json:"document"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Font) == "" {
		writeErr(w, 400, "font required")
		return
	}
	if err := s.c.Affinity().Request(r.Context(), req.Font, req.Document); err != nil {
		writeErr(w, 409, friendly(err))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server string `json:"server"`
		Code   string `json:"code"`
		Name   string `json:"name"`
		CAPEM  string `json:"ca_pem"` // optional: trust a private CA / self-signed cert
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if req.Server == "" || req.Code == "" {
		writeErr(w, 400, "Server address and code are required.")
		return
	}
	caFile := ""
	if pem := strings.TrimSpace(req.CAPEM); pem != "" {
		if !strings.Contains(pem, "-----BEGIN CERTIFICATE-----") {
			writeErr(w, 400, "The certificate must be in PEM format (-----BEGIN CERTIFICATE----- …).")
			return
		}
		dir, err := agent.ConfigDir()
		if err == nil {
			err = os.MkdirAll(dir, 0o700)
		}
		if err == nil {
			caFile = filepath.Join(dir, "ca.pem")
			err = os.WriteFile(caFile, []byte(pem+"\n"), 0o600)
		}
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.c.Enroll(ctx, req.Server, req.Code, req.Name, caFile); err != nil {
		writeErr(w, 400, friendly(err))
		return
	}
	// first connection: make sure fonts come back after a restart
	// (fails harmlessly on macOS when run from outside Applications – the
	// window shows how to fix that)
	if !agent.AutostartEnabled() {
		agent.EnableAutostart("tray", false)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func friendly(err error) string {
	msg := err.Error()
	switch {
	case regexp.MustCompile(`no such host|connection refused|i/o timeout|dial tcp`).MatchString(msg):
		return "Can't reach that server. Check the address and your network."
	case regexp.MustCompile(`HTTP response to HTTPS client`).MatchString(msg):
		return "This server doesn't use HTTPS. Enter the address with http:// in front – or better, ask your administrator to enable HTTPS."
	case regexp.MustCompile(`certificate`).MatchString(msg):
		return "The server's certificate isn't trusted by this computer. If your server uses a private or self-signed certificate, paste its CA certificate under \"Advanced\"."
	case regexp.MustCompile(`invalid or expired`).MatchString(msg):
		return "That code is invalid or has expired. Create a new one under Devices in typehaus."
	}
	return msg
}

func (s *Server) autostart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	var err error
	if req.Enabled {
		_, err = agent.EnableAutostart("tray", false)
	} else {
		err = agent.DisableAutostart(false)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": agent.AutostartEnabled()})
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// font serves an installed font file for previews in the window.
func (s *Server) font(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	sha := r.PathValue("sha")
	if !shaRe.MatchString(sha) {
		http.NotFound(w, r)
		return
	}
	for _, f := range s.c.Installed() {
		if f.SHA256 == sha {
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.Header().Set("Content-Type", "font/sfnt")
			http.ServeFile(w, r, f.Path)
			return
		}
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
