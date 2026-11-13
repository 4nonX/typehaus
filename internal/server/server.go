// Package server implements the typehaus font server: HTTP API, web UI,
// storage and agent endpoints.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// Version is set at build time.
var Version = "dev"

// Config holds runtime configuration, normally read from environment.
type Config struct {
	Listen        string
	DataDir       string
	BaseURL       string
	AdminUser     string
	AdminPassword string
	MaxUploadMB   int64
	PollSeconds   int
	LocalLogin    bool
	SecureCookies bool
	TrustProxy    bool
	SessionHours  int

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCName         string
	OIDCGroupsClaim  string
	OIDCAdminGroup   string
	OIDCAutoCreate   bool

	// AgentsDir holds prebuilt agent binaries offered for download.
	AgentsDir string

	// Downloads lets users download font files directly (admins always can).
	Downloads bool
}

// ConfigFromEnv builds a Config from TYPEHAUS_* variables.
func ConfigFromEnv() Config {
	c := Config{
		Listen:           env("TYPEHAUS_LISTEN", ":8080"),
		DataDir:          env("TYPEHAUS_DATA", "./data"),
		BaseURL:          strings.TrimRight(env("TYPEHAUS_BASE_URL", ""), "/"),
		AdminUser:        env("TYPEHAUS_ADMIN_USER", "admin"),
		AdminPassword:    os.Getenv("TYPEHAUS_ADMIN_PASSWORD"),
		MaxUploadMB:      int64(envInt("TYPEHAUS_MAX_UPLOAD_MB", 512)),
		PollSeconds:      envInt("TYPEHAUS_POLL_SECONDS", 60),
		LocalLogin:       envBool("TYPEHAUS_LOCAL_LOGIN", true),
		TrustProxy:       envBool("TYPEHAUS_TRUST_PROXY", false),
		SessionHours:     envInt("TYPEHAUS_SESSION_HOURS", 24*7),
		OIDCIssuer:       os.Getenv("TYPEHAUS_OIDC_ISSUER"),
		OIDCClientID:     os.Getenv("TYPEHAUS_OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("TYPEHAUS_OIDC_CLIENT_SECRET"),
		OIDCName:         env("TYPEHAUS_OIDC_NAME", "Single sign-on"),
		OIDCGroupsClaim:  os.Getenv("TYPEHAUS_OIDC_GROUPS_CLAIM"),
		OIDCAdminGroup:   os.Getenv("TYPEHAUS_OIDC_ADMIN_GROUP"),
		OIDCAutoCreate:   envBool("TYPEHAUS_OIDC_AUTO_CREATE", true),
		AgentsDir:        env("TYPEHAUS_AGENTS_DIR", "./agents"),
		Downloads:        envBool("TYPEHAUS_DOWNLOADS", true),
	}
	c.SecureCookies = envBool("TYPEHAUS_SECURE_COOKIES", strings.HasPrefix(c.BaseURL, "https://"))
	if c.PollSeconds < 10 {
		c.PollSeconds = 10
	}
	return c
}

// Server is the typehaus application.
type Server struct {
	cfg     Config
	db      *sql.DB
	log     *slog.Logger
	blobDir string
	oidc    *oidcProvider
	limiter *loginLimiter
}

// New opens storage and prepares the server.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Server, error) {
	blobDir := filepath.Join(cfg.DataDir, "fonts")
	if err := os.MkdirAll(blobDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "tmp"), 0o750); err != nil {
		return nil, err
	}
	db, err := openDB(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Server{cfg: cfg, db: db, log: log, blobDir: blobDir, limiter: newLoginLimiter()}
	if err := s.bootstrapAdmin(); err != nil {
		return nil, err
	}
	if cfg.OIDCIssuer != "" {
		if cfg.BaseURL == "" {
			return nil, errors.New("TYPEHAUS_BASE_URL is required when OIDC is enabled")
		}
		p, err := newOIDCProvider(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("oidc: %w", err)
		}
		s.oidc = p
	}
	if !cfg.LocalLogin && s.oidc == nil {
		return nil, errors.New("TYPEHAUS_LOCAL_LOGIN=false requires OIDC to be configured")
	}
	if !cfg.LocalLogin && (cfg.OIDCGroupsClaim == "" || cfg.OIDCAdminGroup == "") {
		// Without local login and without an SSO admin group nobody could
		// ever administer typehaus.
		return nil, errors.New("TYPEHAUS_LOCAL_LOGIN=false requires TYPEHAUS_OIDC_GROUPS_CLAIM and TYPEHAUS_OIDC_ADMIN_GROUP, otherwise nobody can sign in as administrator")
	}
	go s.janitor(ctx)
	return s, nil
}

// Close releases resources.
func (s *Server) Close() error { return s.db.Close() }

func (s *Server) bootstrapAdmin() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pw := s.cfg.AdminPassword
	generated := false
	if pw == "" {
		pw = randomToken(12)
		generated = true
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO users (username, display_name, password_hash, is_admin, created_at) VALUES (?, ?, ?, 1, ?)`,
		s.cfg.AdminUser, "Administrator", hash, now())
	if err != nil {
		return err
	}
	if generated {
		s.log.Warn("created initial admin account – change this password after first login",
			"username", s.cfg.AdminUser, "password", pw)
	} else {
		s.log.Info("created initial admin account", "username", s.cfg.AdminUser)
	}
	return nil
}

// janitor removes expired sessions, enrollment codes and stale temp files.
func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		n := now()
		s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, n)
		s.db.Exec(`DELETE FROM enroll_codes WHERE expires_at < ?`, n)
		tmp := filepath.Join(s.cfg.DataDir, "tmp")
		if ents, err := os.ReadDir(tmp); err == nil {
			for _, e := range ents {
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
					os.Remove(filepath.Join(tmp, e.Name()))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// health
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	// auth
	mux.HandleFunc("GET /api/auth/config", s.handleAuthConfig)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)
	mux.Handle("GET /api/me", s.user(s.handleMe))
	mux.Handle("POST /api/me/password", s.user(s.handleChangePassword))

	// fonts
	mux.Handle("GET /api/fonts", s.user(s.handleListFonts))
	mux.Handle("GET /api/fonts/download", s.user(s.handleDownloadFonts))
	mux.Handle("GET /api/fonts/{id}", s.user(s.handleGetFont))
	mux.Handle("GET /api/fonts/{id}/file", s.user(s.handleFontFile))
	mux.Handle("POST /api/fonts", s.admin(s.handleUpload))
	mux.Handle("PATCH /api/fonts", s.admin(s.handleBulkUpdateFonts))
	mux.Handle("DELETE /api/fonts", s.admin(s.handleDeleteFonts))

	// activation
	mux.Handle("POST /api/activations", s.user(s.handleActivate))
	mux.Handle("DELETE /api/activations", s.user(s.handleDeactivate))

	// collections
	mux.Handle("GET /api/collections", s.user(s.handleListCollections))
	mux.Handle("POST /api/collections", s.admin(s.handleCreateCollection))
	mux.Handle("PATCH /api/collections/{id}", s.admin(s.handleUpdateCollection))
	mux.Handle("POST /api/collections/impact", s.admin(s.handleCollectionImpact))
	mux.Handle("DELETE /api/collections/{id}", s.admin(s.handleDeleteCollection))
	mux.Handle("POST /api/collections/{id}/fonts", s.admin(s.handleCollectionAddFonts))
	mux.Handle("DELETE /api/collections/{id}/fonts", s.admin(s.handleCollectionRemoveFonts))

	// devices
	mux.Handle("GET /api/devices", s.user(s.handleListDevices))
	mux.Handle("DELETE /api/devices/{id}", s.user(s.handleDeleteDevice))
	mux.Handle("POST /api/enroll-codes", s.user(s.handleCreateEnrollCode))

	// admin
	mux.Handle("GET /api/requests", s.user(s.handleListRequests))
	mux.Handle("POST /api/requests", s.user(s.handleCreateRequest))
	mux.Handle("PATCH /api/requests/{id}", s.user(s.handleUpdateRequest))
	mux.Handle("DELETE /api/requests/{id}", s.user(s.handleDeleteRequest))
	mux.Handle("GET /api/users", s.admin(s.handleListUsers))
	mux.Handle("POST /api/users", s.admin(s.handleCreateUser))
	mux.Handle("PATCH /api/users/{id}", s.admin(s.handleUpdateUser))
	mux.Handle("DELETE /api/users/{id}", s.admin(s.handleDeleteUser))
	mux.Handle("GET /api/groups", s.admin(s.handleListGroups))
	mux.Handle("POST /api/groups", s.admin(s.handleCreateGroup))
	mux.Handle("PATCH /api/groups/{id}", s.admin(s.handleUpdateGroup))
	mux.Handle("DELETE /api/groups/{id}", s.admin(s.handleDeleteGroup))
	mux.Handle("GET /api/licenses", s.user(s.handleListLicenses)) // non-admins: licenses of fonts they can see
	mux.Handle("GET /api/licenses/{id}", s.user(s.handleGetLicense))
	mux.Handle("GET /api/licenses/export.csv", s.admin(s.handleLicenseReport))
	mux.Handle("POST /api/licenses", s.admin(s.handleCreateLicense))
	mux.Handle("POST /api/licenses/detect", s.admin(s.handleDetectLicenses))
	mux.Handle("PATCH /api/licenses/{id}", s.admin(s.handleUpdateLicense))
	mux.Handle("DELETE /api/licenses/{id}", s.admin(s.handleDeleteLicense))
	mux.Handle("POST /api/licenses/{id}/documents", s.admin(s.handleUploadLicenseDocs))
	mux.Handle("GET /api/licenses/{id}/documents/{doc}", s.user(s.handleLicenseDoc))
	mux.Handle("PATCH /api/licenses/{id}/documents/{doc}", s.admin(s.handleUpdateLicenseDoc))
	mux.Handle("DELETE /api/licenses/{id}/documents/{doc}", s.admin(s.handleDeleteLicenseDoc))
	mux.Handle("GET /api/reports/usage", s.admin(s.handleUsageReport))
	mux.Handle("GET /api/audit", s.admin(s.handleAudit))

	// agent
	mux.HandleFunc("POST /api/agent/enroll", s.handleAgentEnroll)
	mux.Handle("GET /api/agent/manifest", s.device(s.handleAgentManifest))
	mux.Handle("GET /api/agent/fonts/{sha}", s.device(s.handleAgentFont))
	mux.Handle("POST /api/agent/report", s.device(s.handleAgentReport))

	// agent binaries (public: open-source, needed before a device can authenticate)
	mux.HandleFunc("GET /api/downloads", s.handleListDownloads)
	mux.HandleFunc("GET /downloads/{name}", s.handleDownload)

	// web UI
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path, ".") {
			r.URL.Path = "/" // SPA fallback
		}
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})

	return s.securityHeaders(s.csrf(mux))
}

// ---- middleware ---------------------------------------------------------

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; font-src 'self' blob: data:; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// csrf requires a custom header on state-changing browser requests. Browsers
// cannot send it cross-origin without a CORS preflight, which we never allow.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/agent/") &&
				r.Header.Get("X-Typehaus") != "1" {
				writeErr(w, http.StatusForbidden, "missing X-Typehaus header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxDevice
)

type authUser struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	HasPassword bool
}

type authDevice struct {
	ID       int64
	UserID   int64
	User     string
	Platform string // windows, darwin, linux (from enrollment; "-machine" stripped)
	Agent    string // agent version from the User-Agent header
}

func (s *Server) user(h func(http.ResponseWriter, *http.Request, *authUser)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		h(w, r, u)
	})
}

func (s *Server) admin(h func(http.ResponseWriter, *http.Request, *authUser)) http.Handler {
	return s.user(func(w http.ResponseWriter, r *http.Request, u *authUser) {
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		h(w, r, u)
	})
}

func (s *Server) device(h func(http.ResponseWriter, *http.Request, *authDevice)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			writeErr(w, http.StatusUnauthorized, "missing device token")
			return
		}
		d := &authDevice{}
		var disabled bool
		err := s.db.QueryRow(`SELECT d.id, d.user_id, u.username, u.disabled, d.platform FROM devices d JOIN users u ON u.id = d.user_id
			WHERE d.token_hash = ?`, hashToken(tok)).Scan(&d.ID, &d.UserID, &d.User, &disabled, &d.Platform)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "device not enrolled or revoked")
			return
		}
		d.Platform = strings.TrimSuffix(d.Platform, "-machine")
		if ua := r.Header.Get("User-Agent"); strings.HasPrefix(ua, "typehaus-agent/") {
			d.Agent, _, _ = strings.Cut(strings.TrimPrefix(ua, "typehaus-agent/"), " ")
			d.Agent = trunc(d.Agent, 40)
		}
		// Every request carries the agent version, so an updated agent shows
		// up right away – not only after the next font change, when it sends
		// a report.
		s.db.Exec(`UPDATE devices SET last_seen = ?, last_ip = ?,
			agent_version = CASE WHEN ? != '' THEN ? ELSE agent_version END WHERE id = ?`,
			now(), s.clientIP(r), d.Agent, d.Agent, d.ID)
		if disabled {
			// Suspended, not revoked: the agent removes its fonts but keeps the
			// enrollment, so re-enabling the account restores everything.
			writeErr(w, http.StatusForbidden, "account disabled")
			return
		}
		h(w, r, d)
	})
}

// ---- helpers -------------------------------------------------------------

func (s *Server) audit(u *authUser, action, format string, args ...any) {
	var uid any
	actor := "system"
	if u != nil {
		uid, actor = u.ID, u.Username
	}
	s.db.Exec(`INSERT INTO audit (ts, user_id, actor, action, detail) VALUES (?, ?, ?, ?, ?)`,
		now(), uid, actor, action, fmt.Sprintf(format, args...))
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) blobPath(sha string) string {
	return filepath.Join(s.blobDir, sha[:2], sha)
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

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func (s *Server) internalErr(w http.ResponseWriter, err error) {
	s.log.Error("internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func now() int64 { return time.Now().Unix() }

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="))
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// placeholders returns "?,?,?" for n items.
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	a := make([]any, len(ids))
	for i, v := range ids {
		a[i] = v
	}
	return a
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
