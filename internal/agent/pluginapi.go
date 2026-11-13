package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/protocol"
)

// Plugin API: app plugins (InDesign, Photoshop, Illustrator) and the
// Affinity bridge ask the typehaus app on the same computer about a
// document's fonts. The app forwards to the server with its device token, so
// everything happens for the person signed in here – plugins hold no secret.
//
// It listens on 127.0.0.1 only. Requests must carry X-Typehaus-Plugin: 1 and
// a loopback Host; requests from web pages (an http(s) Origin) are refused,
// and no CORS headers are ever sent, so a website can't use it.

// PluginPort is where plugins find the app (TYPEHAUS_PLUGIN_PORT overrides).
const PluginPort = 47130

func pluginAddr() string {
	port := PluginPort
	if p, err := strconv.Atoi(os.Getenv("TYPEHAUS_PLUGIN_PORT")); err == nil && p > 0 && p < 65536 {
		port = p
	}
	return "127.0.0.1:" + strconv.Itoa(port)
}

// Resolve asks the server what it can do about a document's fonts.
func (a *Agent) Resolve(ctx context.Context, fonts []protocol.FontRef) ([]protocol.FontStatus, error) {
	var out protocol.ResolveResponse
	if err := a.postJSON(ctx, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: fonts}, &out); err != nil {
		return nil, err
	}
	return out.Fonts, nil
}

// ActivateFonts switches fonts on for this computer's person.
func (a *Agent) ActivateFonts(ctx context.Context, ids []int64) error {
	return a.postJSON(ctx, "/api/agent/fonts/activate", map[string]any{"font_ids": ids}, nil)
}

// RequestFont files a font request.
func (a *Agent) RequestFont(ctx context.Context, font, purpose, app string) error {
	return a.postJSON(ctx, "/api/agent/requests", map[string]string{"font": font, "purpose": purpose, "app": app}, nil)
}

func (a *Agent) postJSON(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	resp, err := a.do(ctx, "POST", path, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New(apiError(resp))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// PluginStatus is a font as the plugin API reports it: the server's view
// plus whether the file is installed on this computer yet.
type PluginStatus struct {
	protocol.FontStatus
	Installed bool `json:"installed"`
}

type pluginServer struct {
	log     *slog.Logger
	agent   func() (*Agent, error) // a fresh agent for the current enrollment
	syncNow func()
}

// StartPluginAPI serves the plugin API until ctx ends. A second instance
// (port taken) is not an error: the first one serves.
func StartPluginAPI(ctx context.Context, log *slog.Logger, agent func() (*Agent, error), syncNow func()) {
	ln, err := net.Listen("tcp", pluginAddr())
	if err != nil {
		log.Info("plugin API not started (port in use – another typehaus app serves it)", "addr", pluginAddr(), "err", err)
		return
	}
	p := &pluginServer{log: log, agent: agent, syncNow: syncNow}
	srv := &http.Server{Handler: p.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go srv.Serve(ln)
	log.Info("plugin API listening", "addr", ln.Addr().String())
}

func (p *pluginServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", p.status)
	mux.HandleFunc("POST /v1/fonts/check", p.check)
	mux.HandleFunc("POST /v1/fonts/activate", p.activate)
	mux.HandleFunc("POST /v1/requests", p.request)
	return guardPlugin(mux)
}

// guardPlugin keeps everything but local plugins out.
func guardPlugin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" { // DNS rebinding
			pluginErr(w, 403, "forbidden", "wrong host")
			return
		}
		if o := strings.ToLower(r.Header.Get("Origin")); strings.HasPrefix(o, "http://") || strings.HasPrefix(o, "https://") {
			pluginErr(w, 403, "forbidden", "web pages can't use the typehaus plugin API")
			return
		}
		if r.Header.Get("X-Typehaus-Plugin") != "1" {
			pluginErr(w, 403, "forbidden", "missing X-Typehaus-Plugin header")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

func pluginErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

func pluginJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func (p *pluginServer) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"app": "typehaus", "version": Version, "connected": false}
	if cfg, err := LoadConfig(); err == nil {
		out["connected"], out["server"], out["user"] = true, cfg.Server, cfg.User
	}
	pluginJSON(w, out)
}

type pluginFontsReq struct {
	Fonts []protocol.FontRef `json:"fonts"`
	App   string             `json:"app"`
}

// installedSHAs: font files typehaus has installed on this computer.
func installedSHAs() map[string]bool {
	out := map[string]bool{}
	if st, err := LoadState(); err == nil {
		for sha, f := range st.Fonts {
			if !f.PendingRemoval {
				out[sha] = true
			}
		}
	}
	for sha := range MachineInstalled() {
		out[sha] = true
	}
	return out
}

func (p *pluginServer) resolve(ctx context.Context, w http.ResponseWriter, fonts []protocol.FontRef) (*Agent, []PluginStatus, bool) {
	a, err := p.agent()
	if err != nil {
		pluginErr(w, 409, "not_connected", "typehaus isn't connected on this computer – open typehaus and connect it")
		return nil, nil, false
	}
	if len(fonts) == 0 || len(fonts) > 500 {
		pluginErr(w, 400, "invalid_request", "send 1 to 500 fonts")
		return nil, nil, false
	}
	st, err := a.Resolve(ctx, fonts)
	if err != nil {
		pluginErr(w, 502, "server_unreachable", "the typehaus server can't be reached: "+err.Error())
		return nil, nil, false
	}
	have := installedSHAs()
	out := make([]PluginStatus, len(st))
	for i, f := range st {
		out[i] = PluginStatus{FontStatus: f, Installed: f.SHA256 != "" && have[f.SHA256]}
	}
	return a, out, true
}

func (p *pluginServer) check(w http.ResponseWriter, r *http.Request) {
	var req pluginFontsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		pluginErr(w, 400, "invalid_request", "invalid JSON")
		return
	}
	if _, out, ok := p.resolve(r.Context(), w, req.Fonts); ok {
		pluginJSON(w, map[string]any{"fonts": out})
	}
}

// activate switches the available fonts on, syncs at once and waits (up to
// 45 s) until their files are installed here.
func (p *pluginServer) activate(w http.ResponseWriter, r *http.Request) {
	var req pluginFontsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		pluginErr(w, 400, "invalid_request", "invalid JSON")
		return
	}
	a, out, ok := p.resolve(r.Context(), w, req.Fonts)
	if !ok {
		return
	}
	var ids []int64
	for _, f := range out {
		if f.Status == "available" {
			ids = append(ids, f.FontID)
		}
	}
	if len(ids) > 0 {
		if err := a.ActivateFonts(r.Context(), ids); err != nil {
			pluginErr(w, 409, "refused", err.Error())
			return
		}
		p.log.Info("plugin activated fonts", "app", req.App, "count", len(ids))
	}
	p.syncNow()
	// wait for the files
	want := map[string]bool{}
	for i, f := range out {
		if f.Status == "available" || f.Status == "active" {
			out[i].Status = "active"
			if !f.Installed {
				want[f.SHA256] = true
			}
		}
	}
	deadline := time.Now().Add(45 * time.Second)
	for len(want) > 0 && time.Now().Before(deadline) && r.Context().Err() == nil {
		time.Sleep(300 * time.Millisecond)
		have := installedSHAs()
		for sha := range want {
			if have[sha] {
				delete(want, sha)
			}
		}
	}
	have := installedSHAs()
	for i, f := range out {
		out[i].Installed = f.SHA256 != "" && have[f.SHA256]
	}
	pluginJSON(w, map[string]any{"fonts": out, "complete": len(want) == 0})
}

func (p *pluginServer) request(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Font    string `json:"font"`
		Purpose string `json:"purpose"`
		App     string `json:"app"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Font) == "" {
		pluginErr(w, 400, "invalid_request", "font required")
		return
	}
	a, err := p.agent()
	if err != nil {
		pluginErr(w, 409, "not_connected", "typehaus isn't connected on this computer")
		return
	}
	if err := a.RequestFont(r.Context(), req.Font, req.Purpose, req.App); err != nil {
		pluginErr(w, 409, "refused", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": fmt.Sprintf("Requested %s – your administrators will answer in typehaus.", req.Font)})
}
