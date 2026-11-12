package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type oidcProvider struct {
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

func newOIDCProvider(ctx context.Context, cfg Config) (*oidcProvider, error) {
	p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if cfg.OIDCGroupsClaim != "" {
		scopes = append(scopes, "groups")
	}
	return &oidcProvider{
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  cfg.BaseURL + "/auth/oidc/callback",
			Scopes:       scopes,
		},
	}, nil
}

const oidcStateCookie = "typehaus_oidc"

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	state, nonce, verifier := randomToken(16), randomToken(16), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: state + "." + nonce + "." + verifier, Path: "/auth/oidc",
		MaxAge: 600, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(msg string, err error) {
		s.log.Warn("oidc login failed", "reason", msg, "err", err)
		http.Redirect(w, r, "/?login_error="+strings.ReplaceAll(msg, " ", "+"), http.StatusFound)
	}
	c, err := r.Cookie(oidcStateCookie)
	if err != nil {
		fail("login session expired", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/auth/oidc", MaxAge: -1})
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 || r.URL.Query().Get("state") != parts[0] {
		fail("invalid state", nil)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("provider error "+e, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tok, err := s.oidc.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(parts[2]))
	if err != nil {
		fail("code exchange failed", err)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		fail("no id_token", nil)
		return
	}
	idt, err := s.oidc.verifier.Verify(ctx, raw)
	if err != nil || idt.Nonce != parts[1] {
		fail("token verification failed", err)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail("bad claims", err)
		return
	}
	uid, err := s.upsertOIDCUser(idt.Subject, claims)
	if err != nil {
		fail(err.Error(), err)
		return
	}
	if err := s.startSession(w, uid); err != nil {
		fail("session error", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func claimString(c map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := c[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func claimStrings(c map[string]any, key string) []string {
	switch v := c[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

func (s *Server) upsertOIDCUser(sub string, claims map[string]any) (int64, error) {
	username := claimString(claims, "preferred_username", "email", "sub")
	display := claimString(claims, "name", "preferred_username")
	email := claimString(claims, "email")
	var groups []string
	if s.cfg.OIDCGroupsClaim != "" {
		groups = claimStrings(claims, s.cfg.OIDCGroupsClaim)
	}

	var id int64
	var disabled bool
	err := s.db.QueryRow(`SELECT id, disabled FROM users WHERE oidc_subject = ?`, sub).Scan(&id, &disabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// link to an existing local account with the same username that has no subject yet
		err = s.db.QueryRow(`SELECT id, disabled FROM users WHERE username = ? AND oidc_subject IS NULL`, username).Scan(&id, &disabled)
		if err == nil {
			s.db.Exec(`UPDATE users SET oidc_subject = ? WHERE id = ?`, sub, id)
		} else if errors.Is(err, sql.ErrNoRows) {
			if !s.cfg.OIDCAutoCreate {
				return 0, errors.New("no account for this user")
			}
			res, err := s.db.Exec(`INSERT INTO users (username, display_name, email, oidc_subject, created_at) VALUES (?, ?, ?, ?, ?)`,
				username, display, email, sub, now())
			if err != nil {
				return 0, fmt.Errorf("create user: %w", err)
			}
			id, _ = res.LastInsertId()
			s.audit(&authUser{ID: id, Username: username}, "user.create", "created via OIDC")
		} else {
			return 0, err
		}
	case err != nil:
		return 0, err
	}
	if disabled {
		return 0, errors.New("account disabled")
	}
	s.db.Exec(`UPDATE users SET display_name = ?, email = ? WHERE id = ?`, display, email, id)

	if s.cfg.OIDCGroupsClaim != "" {
		// Sync membership for groups that exist locally (matched by name).
		// Only memberships created by SSO are replaced; groups an admin
		// assigned by hand stay.
		s.db.Exec(`DELETE FROM user_groups WHERE user_id = ? AND source = 'oidc'`, id)
		for _, g := range groups {
			s.db.Exec(`INSERT OR IGNORE INTO user_groups (user_id, group_id, source) SELECT ?, id, 'oidc' FROM groups WHERE name = ?`, id, g)
		}
		s.pruneActivations()
		if s.cfg.OIDCAdminGroup != "" {
			isAdmin := false
			for _, g := range groups {
				if strings.EqualFold(g, s.cfg.OIDCAdminGroup) {
					isAdmin = true
				}
			}
			s.db.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, boolInt(isAdmin), id)
		}
	}
	s.audit(&authUser{ID: id, Username: username}, "login", "OIDC login")
	return id, nil
}
