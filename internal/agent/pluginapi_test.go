package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The plugin API answers local plugins only – never web pages.
func TestPluginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := guardPlugin(ok)
	for _, c := range []struct {
		name, host, origin, header string
		want                       int
	}{
		{"plugin", "127.0.0.1:47130", "", "1", 200},
		{"plugin via localhost", "localhost:47130", "", "1", 200},
		{"UXP/CEP origin", "127.0.0.1:47130", "null", "1", 200},
		{"file origin (CEP)", "127.0.0.1:47130", "file://", "1", 200},
		{"web page", "127.0.0.1:47130", "https://evil.example", "1", 403},
		{"web page over http", "127.0.0.1:47130", "http://localhost:3000", "1", 403},
		{"no plugin header", "127.0.0.1:47130", "", "", 403},
		{"DNS rebinding", "evil.example:47130", "", "1", 403},
	} {
		r := httptest.NewRequest("POST", "/v1/fonts/check", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.header != "" {
			r.Header.Set("X-Typehaus-Plugin", c.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS header sent", c.name)
		}
	}
}
