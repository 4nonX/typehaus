package server

import (
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "typehaus_session"

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

// dummyHash keeps login timing constant for unknown users.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("typehaus-timing-equaliser"), 12)

func (s *Server) sessionUser(r *http.Request) *authUser {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u := &authUser{}
	var pw sql.NullString
	err = s.db.QueryRow(`SELECT u.id, u.username, u.display_name, u.is_admin, u.password_hash
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`,
		hashToken(c.Value), now()).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &pw)
	if err != nil {
		return nil
	}
	u.HasPassword = pw.Valid && pw.String != ""
	return u
}

func (s *Server) startSession(w http.ResponseWriter, userID int64) error {
	tok := randomToken(32)
	exp := time.Now().Add(time.Duration(s.cfg.SessionHours) * time.Hour)
	if _, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(tok), userID, exp.Unix()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"local_login": s.cfg.LocalLogin,
		"oidc":        s.oidc != nil,
		"oidc_name":   s.cfg.OIDCName,
		"version":     Version,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.LocalLogin {
		writeErr(w, http.StatusForbidden, "local login is disabled")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	key := ip + "|" + strings.ToLower(req.Username)
	if !s.limiter.allow(key) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	var id int64
	var hash sql.NullString
	var disabled bool
	err := s.db.QueryRow(`SELECT id, password_hash, disabled FROM users WHERE username = ?`, req.Username).
		Scan(&id, &hash, &disabled)
	if err != nil || !hash.Valid || hash.String == "" {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		s.limiter.fail(key)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash.String), []byte(req.Password)) != nil || disabled {
		s.limiter.fail(key)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.limiter.reset(key)
	if err := s.startSession(w, id); err != nil {
		s.internalErr(w, err)
		return
	}
	s.audit(&authUser{ID: id, Username: req.Username}, "login", "local login from %s", ip)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *authUser) {
	writeJSON(w, 200, map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
		"is_admin": u.IsAdmin, "has_password": u.HasPassword, "version": Version,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.New) < 10 {
		writeErr(w, 400, "new password must be at least 10 characters")
		return
	}
	var hash sql.NullString
	s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash)
	if hash.Valid && hash.String != "" &&
		bcrypt.CompareHashAndPassword([]byte(hash.String), []byte(req.Current)) != nil {
		writeErr(w, 403, "current password is wrong")
		return
	}
	nh, err := hashPassword(req.New)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, nh, u.ID)
	// invalidate other sessions
	cur, _ := r.Cookie(sessionCookie)
	s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, u.ID, hashToken(cur.Value))
	s.audit(u, "password.change", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- brute-force limiter ----------------------------------------------------

type loginLimiter struct {
	mu   sync.Mutex
	hits map[string]*limitEntry
}

type limitEntry struct {
	fails int
	until time.Time
	last  time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{hits: map[string]*limitEntry{}} }

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.hits[key]
	return e == nil || time.Now().After(e.until)
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// opportunistic cleanup
	if len(l.hits) > 10000 {
		for k, e := range l.hits {
			if time.Since(e.last) > time.Hour {
				delete(l.hits, k)
			}
		}
	}
	e := l.hits[key]
	if e == nil {
		e = &limitEntry{}
		l.hits[key] = e
	}
	e.fails++
	e.last = time.Now()
	if e.fails >= 5 {
		backoff := time.Duration(1<<min(e.fails-5, 8)) * 30 * time.Second
		e.until = time.Now().Add(backoff)
	}
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}
