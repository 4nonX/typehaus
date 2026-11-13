package adobeplugins_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4nonX/typehaus/internal/adobeplugins"
	"github.com/4nonX/typehaus/internal/agent"
	"github.com/4nonX/typehaus/internal/protocol"
)

// stub answers the typehaus server's device endpoints.
type stub struct {
	mu        sync.Mutex
	activated []int64
	requests  []map[string]string
	fonts     map[string]protocol.FontStatus
}

func (s *stub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agent/fonts/resolve", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ResolveRequest
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		defer s.mu.Unlock()
		out := protocol.ResolveResponse{}
		for _, f := range req.Fonts {
			st, ok := s.fonts[f.PostScript]
			if !ok {
				st = protocol.FontStatus{Status: "not_available"}
			}
			st.FontRef = f
			out.Fonts = append(out.Fonts, st)
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/agent/fonts/activate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs []int64 `json:"font_ids"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.activated = append(s.activated, req.IDs...)
		for k, f := range s.fonts {
			for _, id := range req.IDs {
				if f.FontID == id {
					f.Status = "active"
					s.fonts[k] = f
				}
			}
		}
		s.mu.Unlock()
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/agent/requests", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	})
	return mux
}

type appEnv struct {
	base   string
	home   string
	bridge string
	stub   *stub
}

// startApp runs the typehaus app's plugin API against a stub server. Its
// "sync" installs Poppins the way a real sync does: the file into the
// user's font folder, recorded in the app's state.
func startApp(t *testing.T) *appEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TYPEHAUS_AGENT_HOME", home)
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("TYPEHAUS_INVENTORY_DIRS", filepath.Join(data, "fonts"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	t.Setenv("TYPEHAUS_PLUGIN_PORT", strconv.Itoa(port))

	poppins, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", "Poppins-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	st := &stub{fonts: map[string]protocol.FontStatus{
		"Poppins-Regular": {Status: "available", FontID: 5, SHA256: "poppinssha", Family: "Poppins", Style: "Regular"},
		"Brand-Bold":      {Status: "refused", FontID: 7, SHA256: "brandsha", Family: "Brand", Style: "Bold", Reason: "all 2 seats are in use"},
	}}
	srv := httptest.NewServer(st.handler())
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := agent.New(&agent.Config{Server: srv.URL, Token: "dev", DeviceID: 1, User: "dana"}, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveConfig(&agent.Config{Server: srv.URL, Token: "dev", DeviceID: 1, User: "dana"}); err != nil {
		t.Fatal(err)
	}
	sync := func() {
		dir := filepath.Join(data, "fonts", "typehaus")
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "Poppins-Regular.ttf")
		os.WriteFile(p, poppins, 0o644)
		state, _ := json.Marshal(map[string]any{"fonts": map[string]any{"poppinssha": map[string]string{
			"sha256": "poppinssha", "path": p, "family": "Poppins", "style": "Regular", "full_name": "Poppins Regular"}}})
		os.WriteFile(filepath.Join(home, "state.json"), state, 0o600)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent.StartPluginAPI(ctx, log, func() (*agent.Agent, error) { return a, nil }, sync)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, err := http.Get(base + "/v1/status"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin API didn't start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &appEnv{base: base, home: home, bridge: filepath.Join(home, "plugins"), stub: st}
}

func runHarness(t *testing.T, env *appEnv, scenario string) map[string]any {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	src, _ := filepath.Abs("src")
	cmd := exec.Command(node, filepath.Join("testdata", "harness.js"), scenario, src)
	cmd.Env = append(os.Environ(), "PLUGIN_BASE="+env.base, "BRIDGE_DIR="+env.bridge)
	out, err := cmd.Output()
	var res map[string]any
	if json.Unmarshal(bytes.TrimSpace(out), &res) != nil || err != nil || res["error"] != nil {
		t.Fatalf("%s: %v\n%s", scenario, err, out)
	}
	return res
}

func rows(v any) map[string]string {
	out := map[string]string{}
	list, _ := v.([]any)
	for _, r := range list {
		x := r.([]any)
		out[x[0].(string)] = x[1].(string)
	}
	return out
}

func TestInDesignPanel(t *testing.T) {
	env := startApp(t)
	res := runHarness(t, env, "indesign")
	got := rows(res["before"])
	want := map[string]string{"Poppins-Regular": "available", "Brand-Bold": "refused", "Futura-Bold": "not_available", "Ghost-Regular": "not_available"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["ArialMT"]; ok {
		t.Fatal("an installed font is listed as missing")
	}
	html := res["html"].(string)
	for _, s := range []string{"Brochure.indd", "4 fonts missing", `data-on="*"`, "Switch on the font", `data-req="brand-bold"`, "all 2 seats are in use"} {
		if !strings.Contains(html, s) {
			t.Fatalf("panel misses %q:\n%s", s, html)
		}
	}
	act := res["activate"].(map[string]any)
	if act["activated"].(float64) != 1 || act["complete"] != true {
		t.Fatalf("activate: %v", act)
	}
	if len(env.stub.activated) != 1 || env.stub.activated[0] != 5 {
		t.Fatalf("server activated %v", env.stub.activated)
	}
	if r := env.stub.requests[0]; r["font"] != "Brand-Bold" || r["app"] != "InDesign" || !strings.Contains(r["purpose"], "Brochure.indd") {
		t.Fatalf("request: %v", r)
	}
	if res["mode"] != "http" {
		t.Fatalf("mode %v", res["mode"])
	}
	events := res["events"].([]any)
	if len(events) != 4 {
		t.Fatalf("events: %v", events)
	}
}

// Photoshop on macOS: no http to the app; everything goes through files.
func TestPhotoshopPanelViaFileBridge(t *testing.T) {
	env := startApp(t)
	res := runHarness(t, env, "photoshop")
	got := rows(res["before"])
	if len(got) != 1 || got["Poppins-Regular"] != "available" {
		t.Fatalf("rows: %v", got)
	}
	if b := res["batches"].([]any); len(b) != 1 || b[0].(float64) != 2 {
		t.Fatalf("one batchPlay for both text layers: %v", b)
	}
	act := res["activate"].(map[string]any)
	if act["activated"].(float64) != 1 || act["complete"] != true || res["mode"] != "file" {
		t.Fatalf("activate via files: %v %v", act, res["mode"])
	}
	if l := res["leftover"].([]any); len(l) != 0 {
		t.Fatalf("files left behind: %v", l)
	}
}

// Illustrator can't tell missing fonts: the app's view of the computer decides.
func TestIllustratorPanel(t *testing.T) {
	env := startApp(t)
	res := runHarness(t, env, "illustrator")
	before := rows(res["before"])
	if before["Poppins-Regular"] != "available" || before["Futura-Bold"] != "not_available" || before[`Quote"Font\Name`] != "not_available" {
		t.Fatalf("before: %v", before)
	}
	after := rows(res["after"])
	if _, ok := after["Poppins-Regular"]; ok {
		t.Fatalf("Poppins still missing after installing: %v", after)
	}
	if res["nodeCalls"].(float64) < 3 {
		t.Fatalf("the panel didn't use CEP's Node.js http: %v", res["nodeCalls"])
	}
	if res["nodoc"] != "nodoc" {
		t.Fatalf("no document: %v", res["nodoc"])
	}
}

func TestNoApp(t *testing.T) {
	env := &appEnv{base: "http://127.0.0.1:9"}
	res := runHarness(t, env, "noapp")
	if res["http"] != "noapp" || res["file"] != "noapp" {
		t.Fatalf("%v", res)
	}
	if l := res["fileLeftover"].([]any); len(l) != 0 {
		t.Fatalf("a request file was left behind: %v", l)
	}
}

func TestPackages(t *testing.T) {
	for _, id := range []string{"indesign", "photoshop"} {
		b, err := adobeplugins.CCX(id, "v1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		var manifest map[string]any
		for _, f := range zr.File {
			names[f.Name] = true
			if f.Name == "manifest.json" {
				rc, _ := f.Open()
				if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
					t.Fatalf("%s manifest: %v", id, err)
				}
				rc.Close()
			}
		}
		for _, n := range []string{"manifest.json", "index.html", "main.js", "host.js", "bridge.js", "typehaus.js", "panel.css", "icons/panel.png", "icons/panel@2x.png", "icons/plugin.png"} {
			if !names[n] {
				t.Fatalf("%s: %s missing", id, n)
			}
		}
		if manifest["version"] != "1.2.3" || manifest["manifestVersion"].(float64) != 5 || manifest["main"] != "index.html" {
			t.Fatalf("%s manifest: %v", id, manifest)
		}
		ep := manifest["entrypoints"].([]any)[0].(map[string]any)
		if ep["id"] != "typehausPanel" {
			t.Fatal("entrypoint id must match main.js")
		}
		perms := manifest["requiredPermissions"].(map[string]any)
		if perms["localFileSystem"] != "fullAccess" || !strings.Contains(strings.Join(toStrings(perms["network"].(map[string]any)["domains"]), ","), "http://127.0.0.1:47130") {
			t.Fatalf("%s permissions: %v", id, perms)
		}
		// every icon the manifest names is in the package
		for _, ic := range append(manifest["icons"].([]any), ep["icons"].([]any)...) {
			if p := ic.(map[string]any)["path"].(string); !names[p] {
				t.Fatalf("%s: icon %s missing", id, p)
			}
		}
	}
	// the Illustrator extension
	files, err := adobeplugins.Files("illustrator", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		BundleID string `xml:"ExtensionBundleId,attr"`
		Version  string `xml:"ExtensionBundleVersion,attr"`
		Hosts    []struct {
			Name string `xml:"Name,attr"`
		} `xml:"ExecutionEnvironment>HostList>Host"`
		Main   string   `xml:"DispatchInfoList>Extension>DispatchInfo>Resources>MainPath"`
		CEF    []string `xml:"DispatchInfoList>Extension>DispatchInfo>Resources>CEFCommandLine>Parameter"`
		Script string   `xml:"DispatchInfoList>Extension>DispatchInfo>Resources>ScriptPath"`
	}
	if err := xml.Unmarshal(files["CSXS/manifest.xml"], &m); err != nil {
		t.Fatal(err)
	}
	if m.BundleID != "com.typehaus.illustrator" || m.Version != "1.2.3" || len(m.Hosts) != 1 || m.Hosts[0].Name != "ILST" ||
		len(m.CEF) != 1 || m.CEF[0] != "--enable-nodejs" {
		t.Fatalf("CEP manifest: %+v", m)
	}
	for _, p := range []string{strings.TrimPrefix(m.Main, "./"), strings.TrimPrefix(m.Script, "./"), "typehaus.js", "panel.css", "icons/panel.png"} {
		if files[p] == nil {
			t.Fatalf("illustrator: %s missing", p)
		}
	}
	if adobeplugins.ByFile("typehaus-photoshop.ccx").App != "Photoshop" || adobeplugins.ByFile("other.zip") != nil {
		t.Fatal("ByFile")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
