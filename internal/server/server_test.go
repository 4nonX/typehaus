package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

func testFont(t *testing.T) (string, []byte) {
	t.Helper()
	for _, pat := range []string{"/usr/share/fonts/truetype/dejavu/*.ttf", "/usr/share/fonts/**/*.ttf", "/usr/share/fonts/*/*/*.otf"} {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			b, err := os.ReadFile(m[0])
			if err == nil {
				return filepath.Base(m[0]), b
			}
		}
	}
	t.Skip("no system font available for testing")
	return "", nil
}

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, hc: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	ct := ""
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
		ct = "application/json"
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("X-Typehaus", "1")
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (c *client) upload(name string, data []byte) []int64 {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/fonts", &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		FontIDs []int64 `json:"font_ids"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || len(out.FontIDs) == 0 {
		c.t.Fatalf("upload failed: %d %+v", resp.StatusCode, out)
	}
	return out.FontIDs
}

func TestEndToEnd(t *testing.T) {
	name, data := testFont(t)
	dir := t.TempDir()
	cfg := Config{DataDir: dir, AdminUser: "admin", AdminPassword: "adminpassword", PollSeconds: 60,
		LocalLogin: true, SessionHours: 1, MaxUploadMB: 64, AgentsDir: dir, Downloads: true}
	s, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	admin := newClient(t, ts.URL)
	if code := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong"}, nil); code != 401 {
		t.Fatalf("bad password: want 401, got %d", code)
	}
	if code := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "adminpassword"}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}

	// CSRF header enforced
	req, _ := http.NewRequest("POST", ts.URL+"/api/groups", bytes.NewReader([]byte(`{"name":"x"}`)))
	resp, _ := admin.hc.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: want 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	ids := admin.upload(name, data)
	// duplicate upload returns the same ids
	if again := admin.upload(name, data); again[0] != ids[0] {
		t.Fatalf("duplicate upload should return existing id")
	}

	var g, col, usr map[string]int64
	admin.do("POST", "/api/groups", map[string]string{"name": "Design"}, &g)
	admin.do("POST", "/api/collections", map[string]any{"name": "Brand", "group_ids": []int64{g["id"]}}, &col)
	admin.do("POST", "/api/users", map[string]any{"username": "bob", "password": "bobpassword1"}, &usr)

	bob := newClient(t, ts.URL)
	bob.do("POST", "/api/auth/login", map[string]string{"username": "bob", "password": "bobpassword1"}, nil)

	// not visible yet: activation is a no-op
	var act map[string]int64
	bob.do("POST", "/api/activations", map[string]any{"font_ids": ids}, &act)
	if act["activated"] != 0 {
		t.Fatalf("bob activated an invisible font")
	}

	// grant access via group + collection
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": ids}, nil)
	admin.do("PATCH", "/api/users/"+itoa(usr["id"]), map[string]any{"group_ids": []int64{g["id"]}}, nil)
	bob.do("POST", "/api/activations", map[string]any{"font_ids": ids}, &act)
	if act["activated"] != int64(len(ids)) {
		t.Fatalf("activation failed: %+v", act)
	}

	// enroll a device
	var code map[string]any
	bob.do("POST", "/api/enroll-codes", nil, &code)
	dev := newClient(t, ts.URL)
	var er protocol.EnrollResponse
	if st := dev.do("POST", "/api/agent/enroll", protocol.EnrollRequest{Code: code["code"].(string), Platform: "linux"}, &er); st != 201 {
		t.Fatalf("enroll: %d", st)
	}
	// code is single use
	if st := dev.do("POST", "/api/agent/enroll", protocol.EnrollRequest{Code: code["code"].(string)}, nil); st != 401 {
		t.Fatalf("reused code: want 401, got %d", st)
	}

	manifest := func() (int, protocol.Manifest) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/agent/manifest", nil)
		req.Header.Set("Authorization", "Bearer "+er.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m protocol.Manifest
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	st, m := manifest()
	if st != 200 || len(m.Fonts) != 1 {
		t.Fatalf("manifest: %d %+v", st, m)
	}

	// download is allowed and byte-identical
	req, _ = http.NewRequest("GET", ts.URL+"/api/agent/fonts/"+m.Fonts[0].SHA256, nil)
	req.Header.Set("Authorization", "Bearer "+er.Token)
	resp, _ = http.DefaultClient.Do(req)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("downloaded font differs")
	}

	// removing bob from the group prunes the activation → empty manifest
	admin.do("PATCH", "/api/users/"+itoa(usr["id"]), map[string]any{"group_ids": []int64{}}, nil)
	if _, m = manifest(); len(m.Fonts) != 0 {
		t.Fatalf("manifest should be empty after access revoked, got %d", len(m.Fonts))
	}

	// revoke device → 401
	admin.do("DELETE", "/api/devices/"+itoa(er.DeviceID), nil, nil)
	if st, _ = manifest(); st != 401 {
		t.Fatalf("revoked device: want 401, got %d", st)
	}

	// non-admin cannot reach admin endpoints
	if st := bob.do("GET", "/api/users", nil, nil); st != 403 {
		t.Fatalf("bob /api/users: want 403, got %d", st)
	}
}

func itoa(i int64) string { return string(jsonNum(i)) }

func jsonNum(i int64) []byte { b, _ := json.Marshal(i); return b }

func newTestServer(t *testing.T) (*Server, *httptest.Server, *client) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{DataDir: dir, AdminUser: "admin", AdminPassword: "adminpassword", PollSeconds: 60,
		LocalLogin: true, SessionHours: 1, MaxUploadMB: 64, AgentsDir: dir, Downloads: true}
	s, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	admin := newClient(t, ts.URL)
	if code := admin.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "adminpassword"}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}
	return s, ts, admin
}

func enrollDevice(t *testing.T, ts *httptest.Server, user *client) string {
	t.Helper()
	var code map[string]any
	user.do("POST", "/api/enroll-codes", nil, &code)
	var er protocol.EnrollResponse
	if st := newClient(t, ts.URL).do("POST", "/api/agent/enroll", protocol.EnrollRequest{Code: code["code"].(string)}, &er); st != 201 {
		t.Fatalf("enroll: %d", st)
	}
	return er.Token
}

func getManifest(t *testing.T, ts *httptest.Server, tok string) (int, protocol.Manifest) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/api/agent/manifest", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m protocol.Manifest
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// Disabling a user suspends their devices (403, enrollment kept);
// enabling them again restores the manifest.
func TestSuspendResume(t *testing.T) {
	name, data := testFont(t)
	_, ts, admin := newTestServer(t)
	ids := admin.upload(name, data)
	var col, usr map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": ids}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "carla", "password": "carlapassword"}, &usr)
	carla := newClient(t, ts.URL)
	carla.do("POST", "/api/auth/login", map[string]string{"username": "carla", "password": "carlapassword"}, nil)
	carla.do("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)
	tok := enrollDevice(t, ts, carla)

	admin.do("PATCH", "/api/users/"+itoa(usr["id"]), map[string]any{"disabled": true}, nil)
	if st, _ := getManifest(t, ts, tok); st != 403 {
		t.Fatalf("disabled user: want 403, got %d", st)
	}
	admin.do("PATCH", "/api/users/"+itoa(usr["id"]), map[string]any{"disabled": false}, nil)
	st, m := getManifest(t, ts, tok)
	if st != 200 || len(m.Fonts) != 1 || m.ServerVersion == "" {
		t.Fatalf("re-enabled: %d %+v", st, m)
	}
}

// Two files with the same PostScript name are never active together.
func TestConflictResolution(t *testing.T) {
	name, data := testFont(t)
	_, ts, admin := newTestServer(t)
	v1 := admin.upload(name, data)
	// same font, different bytes (e.g. another version): trailing padding
	v2 := admin.upload("v2-"+name, append(append([]byte{}, data...), 0, 0, 0, 0))
	if v1[0] == v2[0] {
		t.Fatal("expected a second font record")
	}
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	for _, f := range fonts {
		if !f.Conflict {
			t.Errorf("font %d should be flagged as conflicting", f.ID)
		}
	}
	var r map[string]int64
	admin.do("POST", "/api/activations", map[string]any{"font_ids": v1}, &r)
	admin.do("POST", "/api/activations", map[string]any{"font_ids": v2}, &r)
	if r["replaced"] != 1 {
		t.Fatalf("activating v2 should switch v1 off, got %+v", r)
	}
	tok := enrollDevice(t, ts, admin)
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 1 || m.Fonts[0].ID != v2[0] {
		t.Fatalf("manifest should contain only v2: %+v", m.Fonts)
	}
	// both requested at once: newest wins
	admin.do("POST", "/api/activations", map[string]any{"font_ids": []int64{v1[0], v2[0]}}, &r)
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 1 || m.Fonts[0].ID != v2[0] {
		t.Fatalf("manifest should contain only v2 after bulk activation: %+v", m.Fonts)
	}
}

func TestSSOOnlyRequiresAdminGroup(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminUser: "admin", LocalLogin: false, SessionHours: 1}
	if _, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected startup error without local login and without OIDC")
	}
}

func uploadFiles(t *testing.T, c *client, files map[string][]byte) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for n, d := range files {
		fw, _ := mw.CreateFormFile("files", n)
		fw.Write(d)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/fonts", &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Results []map[string]any `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Results
}

func fixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every format: converted web fonts, Windows bitmap fonts, PostScript Type 1
// with metrics – and each device only gets what its OS can install.
func TestAllFormatsAndPlatforms(t *testing.T) {
	_, ts, admin := newTestServer(t)
	pfa := []byte("%!PS-AdobeFont-1.0: Demo-Regular 001.000\n/FamilyName (Demo) readonly def\n/FullName (Demo Regular) readonly def\n/Weight (Regular) readonly def\n/FontName /Demo-Regular def\n")
	results := uploadFiles(t, admin, map[string][]byte{
		"Poppins-Regular.woff2":  fixture(t, "Poppins-Regular.woff2"),
		"TypehausTestBitmap.fon": fixture(t, "TypehausTestBitmap.fon"),
		"Demo-Regular.pfa":       pfa,
		"Demo-Regular.afm":       []byte("StartFontMetrics 4.1\nFontName Demo-Regular\nEndFontMetrics\n"),
		"Demo-Regular.pfm":       make([]byte, 200),
		"Orphan.afm":             []byte("StartFontMetrics 4.1\n"),
		"notes.txt":              []byte("not a font"),
	})
	status := map[string]string{}
	for _, r := range results {
		status[r["file"].(string)] = r["status"].(string)
	}
	t.Logf("results: %v", results)
	if status["Poppins-Regular.woff2 → Poppins-Regular.ttf"] != "added" || status["TypehausTestBitmap.fon"] != "added" ||
		status["Demo-Regular.pfa"] != "added" || status["orphan.afm"] != "error" {
		t.Fatalf("unexpected upload results: %v", status)
	}

	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	var ids []int64
	kinds := map[string]string{}
	for _, f := range fonts {
		ids = append(ids, f.ID)
		kinds[f.Family] = f.Kind + "|" + f.Platforms + "|" + f.SourceFormat
	}
	if kinds["Poppins"] != "sfnt||woff2" || kinds["Typehaus Test Bitmap"] != "winfnt|windows,linux|" || kinds["Demo"] != "type1|windows,linux|" {
		t.Fatalf("kinds/platforms: %v", kinds)
	}
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": ids}, nil)
	admin.do("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)

	enrollAs := func(platform string) string {
		var code map[string]any
		admin.do("POST", "/api/enroll-codes", nil, &code)
		var er protocol.EnrollResponse
		newClient(t, ts.URL).do("POST", "/api/agent/enroll", protocol.EnrollRequest{Code: code["code"].(string), Platform: platform}, &er)
		return er.Token
	}
	families := func(m protocol.Manifest) map[string]protocol.ManifestFont {
		out := map[string]protocol.ManifestFont{}
		for _, f := range m.Fonts {
			out[f.Family] = f
		}
		return out
	}
	_, mWin := getManifest(t, ts, enrollAs("windows"))
	_, mMac := getManifest(t, ts, enrollAs("darwin"))
	_, mLin := getManifest(t, ts, enrollAs("linux"))
	w, m, l := families(mWin), families(mMac), families(mLin)
	if len(w) != 3 || len(l) != 3 {
		t.Fatalf("windows/linux should get all 3 fonts: %v / %v", w, l)
	}
	if len(m) != 1 || m["Poppins"].ID == 0 {
		t.Fatalf("macOS should only get the OpenType font: %v", m)
	}
	if d := w["Demo"]; d.Kind != "type1" || len(d.Extras) != 2 {
		t.Fatalf("Type 1 manifest entry should carry .afm and .pfm: %+v", d)
	}
	// the agent may download the companion files
	req, _ := http.NewRequest("GET", ts.URL+"/api/agent/fonts/"+w["Demo"].Extras[0].SHA256, nil)
	req.Header.Set("Authorization", "Bearer "+enrollAs("windows"))
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("companion download: %d", resp.StatusCode)
	}
	// an old agent (0.2.x) doesn't get Type 1
	req, _ = http.NewRequest("GET", ts.URL+"/api/agent/manifest", nil)
	req.Header.Set("Authorization", "Bearer "+enrollAs("windows"))
	req.Header.Set("User-Agent", "typehaus-agent/0.2.0 (windows; amd64)")
	resp, _ = http.DefaultClient.Do(req)
	var old protocol.Manifest
	json.NewDecoder(resp.Body).Decode(&old)
	resp.Body.Close()
	if _, has := families(old)["Demo"]; has || len(old.Fonts) != 2 {
		t.Fatalf("agent 0.2.0 must not get Type 1: %+v", old.Fonts)
	}
}
