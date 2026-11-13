package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The file bridge: Photoshop on macOS lets plugins use https only, so a
// plugin there can't reach the plugin API on http://127.0.0.1. Instead it
// writes a request file into the typehaus folder and reads the answer file:
//
//	<config dir>/plugins/requests/<id>.json  {"method","path","body"}
//	<config dir>/plugins/answers/<id>.json   {"status","body"}
//
// (<config dir>: ~/Library/Application Support/typehaus on macOS,
// %APPDATA%\typehaus on Windows.) The app answers through the same
// handlers as the HTTP API. Only processes of the same user can write
// there – the same reach as the loopback port.

var bridgeID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type bridgeRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type bridgeAnswer struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func bridgeDirs() (string, string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", "", err
	}
	req, ans := filepath.Join(d, "plugins", "requests"), filepath.Join(d, "plugins", "answers")
	for _, p := range []string{req, ans} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return "", "", err
		}
	}
	return req, ans, nil
}

// runFileBridge answers request files until ctx ends.
func (p *pluginServer) runFileBridge(ctx context.Context, h http.Handler) {
	reqDir, ansDir, err := bridgeDirs()
	if err != nil {
		p.log.Warn("plugin file bridge not started", "err", err)
		return
	}
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.bridgeRound(h, reqDir, ansDir)
	}
}

func (p *pluginServer) bridgeRound(h http.Handler, reqDir, ansDir string) {
	ents, err := os.ReadDir(reqDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		id := strings.TrimSuffix(name, ".json")
		full := filepath.Join(reqDir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !strings.HasSuffix(name, ".json") || !bridgeID.MatchString(id) {
			if time.Since(info.ModTime()) > time.Minute {
				os.Remove(full)
			}
			continue
		}
		b, err := os.ReadFile(full)
		var req bridgeRequest
		if err != nil || json.Unmarshal(b, &req) != nil {
			// still being written – or broken: give up after a while
			if time.Since(info.ModTime()) > 10*time.Second {
				os.Remove(full)
			}
			continue
		}
		if os.Remove(full) != nil {
			continue
		}
		go func() {
			out, _ := json.Marshal(p.bridgeServe(h, req))
			tmp := filepath.Join(ansDir, id+".tmp")
			if os.WriteFile(tmp, out, 0o600) == nil {
				os.Rename(tmp, filepath.Join(ansDir, id+".json"))
			}
		}()
	}
	// answers nobody picked up
	if ents, err := os.ReadDir(ansDir); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 2*time.Minute {
				os.Remove(filepath.Join(ansDir, e.Name()))
			}
		}
	}
}

func (p *pluginServer) bridgeServe(h http.Handler, req bridgeRequest) bridgeAnswer {
	if req.Method != "GET" && req.Method != "POST" || !strings.HasPrefix(req.Path, "/v1/") {
		return bridgeAnswer{Status: 400, Body: json.RawMessage(`{"error":"bad request","code":"invalid_request"}`)}
	}
	body := []byte(req.Body)
	if len(body) == 0 || string(body) == "null" {
		body = nil
	}
	r := httptest.NewRequest(req.Method, "http://127.0.0.1"+req.Path, bytes.NewReader(body))
	r.Host = "127.0.0.1"
	r.Header.Set("X-Typehaus-Plugin", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := bytes.TrimSpace(w.Body.Bytes())
	if !json.Valid(out) {
		out, _ = json.Marshal(map[string]string{"error": string(out)})
	}
	return bridgeAnswer{Status: w.Code, Body: out}
}
