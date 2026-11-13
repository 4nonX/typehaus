package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

func devicePost(t *testing.T, base, tok, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if out != nil {
		json.Unmarshal(rb, out)
	}
	return resp.StatusCode
}

// What a plugin sees for a document's fonts, and switching them on.
func TestPluginEndpoints(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{
		"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf"),
	})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	poppins := fonts[0]
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{poppins.ID}}, nil)

	admin.do("POST", "/api/users", map[string]any{"username": "gil", "password": "gilpassword1"}, nil)
	gil := newClient(t, ts.URL)
	gil.do("POST", "/api/auth/login", map[string]string{"username": "gil", "password": "gilpassword1"}, nil)
	dev := enrollDevice(t, ts, gil)

	doc := []protocol.FontRef{
		{PostScript: poppins.PostScriptName},
		{Family: "poppins", Style: "regular"},
		{PostScript: "Futura-Bold", Family: "Futura", Style: "Bold"},
	}
	var res protocol.ResolveResponse
	if st := devicePost(t, ts.URL, dev, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: doc}, &res); st != 200 {
		t.Fatalf("resolve: %d", st)
	}
	if len(res.Fonts) != 3 || res.Fonts[0].Status != "available" || res.Fonts[0].FontID != poppins.ID ||
		res.Fonts[1].Status != "available" || res.Fonts[1].FontID != poppins.ID || res.Fonts[2].Status != "not_available" {
		t.Fatalf("resolve: %+v", res.Fonts)
	}

	// switch on → active, and the manifest has it
	var act map[string]any
	if st := devicePost(t, ts.URL, dev, "/api/agent/fonts/activate", map[string]any{"font_ids": []int64{poppins.ID}}, &act); st != 200 {
		t.Fatalf("activate: %d %v", st, act)
	}
	res = protocol.ResolveResponse{}
	devicePost(t, ts.URL, dev, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: doc[:1]}, &res)
	if res.Fonts[0].Status != "active" {
		t.Fatalf("after activate: %+v", res.Fonts[0])
	}
	_, m := getManifest(t, ts, dev)
	if len(m.Fonts) != 1 {
		t.Fatalf("manifest: %+v", m.Fonts)
	}
	var log []auditEntry
	admin.do("GET", "/api/audit", nil, &log)
	if !strings.Contains(log[0].Actor, "(plugin)") || log[0].Action != "font.activate" {
		t.Fatalf("audit: %+v", log[0])
	}

	// license refuses: enforced, one seat, held by the admin
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "One", "seats": 1, "enforce": true}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{poppins.ID}, "license_id": lic["id"]}, nil)
	devicePost(t, ts.URL, dev, "/api/agent/fonts/activate", nil, nil)
	gil.do("DELETE", "/api/activations", map[string]any{"font_ids": []int64{poppins.ID}}, nil)
	admin.do("POST", "/api/activations", map[string]any{"font_ids": []int64{poppins.ID}}, nil)
	res = protocol.ResolveResponse{}
	devicePost(t, ts.URL, dev, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: doc[:1]}, &res)
	if res.Fonts[0].Status != "refused" || !strings.Contains(res.Fonts[0].Reason, "seats") {
		t.Fatalf("refused: %+v", res.Fonts[0])
	}

	// request a missing font, noting the app
	if st := devicePost(t, ts.URL, dev, "/api/agent/requests", map[string]any{"font": "Futura Bold", "app": "InDesign"}, nil); st != 201 {
		t.Fatalf("request: %d", st)
	}
	var reqs []fontRequest
	admin.do("GET", "/api/requests", nil, &reqs)
	if len(reqs) != 1 || reqs[0].User != "gil" || !strings.Contains(reqs[0].Purpose, "InDesign") {
		t.Fatalf("request: %+v", reqs)
	}

	// a font in a collection gil can't see is just "not available"
	var hidden map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "Secret"}, &hidden)
	admin.do("DELETE", "/api/collections/"+itoa(col["id"]), nil, nil)
	admin.do("POST", "/api/collections/"+itoa(hidden["id"])+"/fonts", map[string]any{"font_ids": []int64{poppins.ID}}, nil)
	res = protocol.ResolveResponse{}
	devicePost(t, ts.URL, dev, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: doc[:1]}, &res)
	if res.Fonts[0].Status != "not_available" || res.Fonts[0].FontID != 0 {
		t.Fatalf("hidden font leaks: %+v", res.Fonts[0])
	}
}
