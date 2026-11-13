// Command e2e runs an end-to-end test of server + agent on the current OS:
// it starts a real server, uploads and activates a font, enrolls the agent,
// and checks that the font is installed where the OS (and applications)
// expect it – then deactivates it and checks the removal. On Windows,
// -machine additionally tests the all-users service mode (needs admin).
//
//	go run ./tools/e2e -server ./typehaus-server -agent ./typehaus-agent -font some.ttf [-machine]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/agent/affinitytest"
)

var (
	serverBin = flag.String("server", "", "path to typehaus-server")
	agentBin  = flag.String("agent", "", "path to typehaus-agent")
	fontPath  = flag.String("font", "", "font file(s) to use, comma-separated (any format; uploaded together)")
	machine   = flag.Bool("machine", false, "also test the Windows all-users service (admin)")
)

type env struct {
	base   string
	hc     *http.Client
	home   string // agent config dir
	extra  []string
	family string
}

func main() {
	flag.Parse()
	if *serverBin == "" || *agentBin == "" || *fontPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	tmp, err := os.MkdirTemp("", "typehaus-e2e-")
	check(err)
	defer os.RemoveAll(tmp)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr

	srv := exec.Command(*serverBin)
	srv.Env = append(os.Environ(),
		"TYPEHAUS_DATA="+filepath.Join(tmp, "data"), "TYPEHAUS_LISTEN="+addr, "TYPEHAUS_BASE_URL="+base,
		"TYPEHAUS_ADMIN_PASSWORD=e2e-password-123", "TYPEHAUS_POLL_SECONDS=10", "TYPEHAUS_TRUST_PROXY=false", "TYPEHAUS_INVENTORY=true")
	srvLog, _ := os.Create(filepath.Join(tmp, "server.log"))
	srv.Stdout, srv.Stderr = srvLog, srvLog
	check(srv.Start())
	defer func() {
		srv.Process.Kill()
		if failed {
			b, _ := os.ReadFile(filepath.Join(tmp, "server.log"))
			fmt.Println("---- server log ----\n" + string(b))
		}
	}()
	waitFor("server", 20*time.Second, func() bool {
		r, err := http.Get(base + "/healthz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	})

	jar, _ := cookiejar.New(nil)
	e := &env{base: base, hc: &http.Client{Jar: jar, Timeout: 30 * time.Second}, home: filepath.Join(tmp, "agent")}
	e.extra = []string{"TYPEHAUS_AGENT_HOME=" + e.home, "XDG_DATA_HOME=" + filepath.Join(tmp, "xdg-data"), "XDG_CONFIG_HOME=" + filepath.Join(tmp, "xdg-config")}

	step("sign in and upload " + *fontPath)
	e.api("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "e2e-password-123"}, nil)
	// CI takes some test fonts from this computer's own font folders, so the
	// app would rightly refuse to install them a second time. Install anyway
	// to exercise every format; the "existing" step checks the default.
	e.api("PUT", "/api/settings", map[string]any{"existing_fonts": "install"}, nil)
	ids := e.upload(strings.Split(*fontPath, ","))
	var fonts []struct {
		ID        int64  `json:"id"`
		SHA256    string `json:"sha256"`
		Family    string `json:"family"`
		Kind      string `json:"kind"`
		Platforms string `json:"platforms"`
	}
	e.api("GET", "/api/fonts", nil, &fonts)
	if len(ids) == 0 || len(fonts) == 0 {
		fail("nothing imported")
	}
	expected := map[string]bool{} // font files this OS must install
	for _, f := range fonts {
		fmt.Printf("library: %s (%s, platforms %q)\n", f.Family, f.Kind, f.Platforms)
		if f.Platforms == "" || strings.Contains(","+f.Platforms+",", ","+runtime.GOOS+",") {
			expected[f.SHA256] = true
		}
		if f.Kind == "sfnt" && e.family == "" {
			e.family = f.Family
		}
	}
	var col map[string]int64
	e.api("POST", "/api/collections", map[string]any{"name": "E2E", "everyone": true}, &col)
	e.api("POST", fmt.Sprintf("/api/collections/%d/fonts", col["id"]), map[string]any{"font_ids": ids}, nil)
	e.api("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)

	step("enroll the agent and install for this user")
	e.agent("enroll", "--server", base, "--code", e.code(), "--name", "e2e")
	inst := e.installed(e.home)
	if len(inst) != len(expected) {
		fail("expected %d installed font file(s) on %s, got %d", len(expected), runtime.GOOS, len(inst))
	}
	for _, f := range inst {
		if !expected[f.SHA256] {
			fail("%s should not be installed on %s", f.Path, runtime.GOOS)
		}
		verifyUserInstalled(e, f)
	}
	out := e.agent("status")
	if !strings.Contains(out, fmt.Sprintf("Installed:  %d font file", len(expected))) {
		fail("status output unexpected:\n%s", out)
	}

	step("offline lease: fonts leave after the allowed time offline and come back")
	inst = e.offlineLease(ids, inst)

	step("inventory: fonts copied in by hand are reported, typehaus' own are not")
	e.inventory(ids, inst, tmp)

	step("fonts already on the computer are never installed a second time")
	inst = e.existing(ids, inst, tmp)

	step("deactivate and remove")
	e.api("DELETE", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	if n := len(e.installed(e.home)); n != 0 {
		fail("expected 0 installed fonts after deactivation, got %d", n)
	}
	for _, f := range inst {
		verifyUserRemoved(e, f)
	}

	step("plugin API: a document's missing fonts are switched on through the running app")
	e.pluginFlow(expected)

	step("Affinity: fonts an open document misses are switched on through Affinity's MCP server")
	e.api("DELETE", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	if n := len(e.installed(e.home)); n != 0 {
		fail("expected 0 installed fonts before the Affinity step, got %d", n)
	}
	e.affinityFlow(expected)

	step("revoke the device: agent must refuse to sync")
	e.api("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	var devs []struct {
		ID int64 `json:"id"`
	}
	e.api("GET", "/api/devices", nil, &devs)
	for _, d := range devs {
		e.api("DELETE", fmt.Sprintf("/api/devices/%d", d.ID), nil, nil)
	}
	e.agentMayFail("sync")
	// the CLI sync reports the revocation; removal happens in run/tray mode
	e.agent("unenroll")
	if n := len(e.installed(e.home)); n != 0 {
		fail("unenroll should remove all fonts, %d left", n)
	}
	for _, f := range inst {
		verifyUserRemoved(e, f)
	}

	if *machine {
		step("machine-wide service (all users)")
		testMachine(e, ids, len(expected))
	}
	fmt.Println("\nE2E PASSED")
}

// ---- helpers -------------------------------------------------------------------

var failed bool

func fail(format string, a ...any) {
	failed = true
	msg := fmt.Sprintf(format, a...)
	fmt.Println("FAIL: " + msg)
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::error title=e2e %s::%s\n", runtime.GOOS, strings.ReplaceAll(msg, "\n", " | "))
	}
	panic("e2e failed")
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func step(s string) { fmt.Printf("\n==> %s\n", s) }

func waitFor(what string, d time.Duration, ok func() bool) {
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			fail("timed out waiting for %s", what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (e *env) api(method, path string, body, out any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.hc.Do(req)
	check(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fail("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		check(json.Unmarshal(b, out))
	}
}

func (e *env) upload(paths []string) []int64 {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range paths {
		data, err := os.ReadFile(strings.TrimSpace(p))
		check(err)
		fw, _ := mw.CreateFormFile("files", filepath.Base(p))
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.base+"/api/fonts", &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.hc.Do(req)
	check(err)
	defer resp.Body.Close()
	var out struct {
		FontIDs []int64 `json:"font_ids"`
		Results []struct {
			Status, Message string
		} `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("upload results: %+v\n", out.Results)
	for _, r := range out.Results {
		if r.Status == "error" {
			fail("upload error: %+v", r)
		}
	}
	return out.FontIDs
}

func (e *env) code() string {
	var c map[string]any
	e.api("POST", "/api/enroll-codes", nil, &c)
	return c["code"].(string)
}

func (e *env) run(allowFail bool, args ...string) string {
	cmd := exec.Command(*agentBin, args...)
	cmd.Env = append(os.Environ(), e.extra...)
	out, err := cmd.CombinedOutput()
	fmt.Printf("$ typehaus-agent %s\n%s", strings.Join(args, " "), out)
	if err != nil && !allowFail {
		fail("agent %v: %v", args, err)
	}
	return string(out)
}

func (e *env) agent(args ...string) string        { return e.run(false, args...) }
func (e *env) agentMayFail(args ...string) string { return e.run(true, args...) }

type installed struct {
	SHA256   string   `json:"sha256"`
	Path     string   `json:"path"`
	FullName string   `json:"full_name"`
	Family   string   `json:"family"`
	RegName  string   `json:"reg_name"`
	Kind     string   `json:"kind"`
	Extras   []string `json:"extras"`
}

func (e *env) installed(dir string) []installed {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil
	}
	var st struct {
		Fonts map[string]installed `json:"fonts"`
	}
	check(json.Unmarshal(b, &st))
	var out []installed
	for _, f := range st.Fonts {
		out = append(out, f)
	}
	return out
}

// offlineLease gives the fonts a license with a one-day offline lease, makes
// the server unreachable with the last contact two days ago, and checks that
// the fonts are removed – and installed again once the server is back.
func (e *env) offlineLease(ids []int64, inst []installed) []installed {
	var lic map[string]int64
	e.api("POST", "/api/licenses", map[string]any{"name": "E2E lease", "kind": "commercial", "lease_days": 1}, &lic)
	e.api("PATCH", "/api/fonts", map[string]any{"font_ids": ids, "license_id": lic["id"]}, nil)
	e.agent("sync", "--force")
	for _, f := range e.state()["fonts"].(map[string]any) {
		if d, _ := f.(map[string]any)["lease_days"].(float64); d != 1 {
			fail("agent did not record the lease: %v", f)
		}
	}

	cfgPath := filepath.Join(e.home, "agent.json")
	orig, err := os.ReadFile(cfgPath)
	check(err)
	var cfg map[string]any
	check(json.Unmarshal(orig, &cfg))
	cfg["server"] = "http://127.0.0.1:9" // nothing listens there
	b, _ := json.Marshal(cfg)
	check(os.WriteFile(cfgPath, b, 0o600))
	st := e.state()
	st["last_sync"] = time.Now().Add(-49 * time.Hour).Format(time.RFC3339)
	b, _ = json.Marshal(st)
	check(os.WriteFile(filepath.Join(e.home, "state.json"), b, 0o600))

	e.agentMayFail("sync")
	if n := len(e.installed(e.home)); n != 0 {
		fail("lease ended: expected 0 installed fonts, got %d", n)
	}
	for _, f := range inst {
		verifyUserRemoved(e, f)
	}
	if p, _ := e.state()["paused"].([]any); len(p) != len(inst) {
		fail("expected %d paused fonts, got %v", len(inst), p)
	}
	if out := e.agent("status"); !strings.Contains(out, "Paused:") {
		fail("status should mention paused fonts:\n%s", out)
	}

	check(os.WriteFile(cfgPath, orig, 0o600))
	e.agent("sync")
	back := e.installed(e.home)
	if len(back) != len(inst) {
		fail("server back: expected %d fonts again, got %d", len(inst), len(back))
	}
	for _, f := range back {
		verifyUserInstalled(e, f)
	}
	if p, _ := e.state()["paused"].([]any); len(p) != 0 {
		fail("paused list should be empty after reconnect: %v", p)
	}
	e.api("PATCH", "/api/fonts", map[string]any{"font_ids": ids, "license_id": 0}, nil)
	e.api("DELETE", fmt.Sprintf("/api/licenses/%d", lic["id"]), nil, nil)
	return back
}

// inventory copies a library font into a scanned folder by hand and checks
// that the server lists it as an unmanaged copy of a licensed family, while
// the files typehaus installed itself (also scanned) are left out.
func (e *env) inventory(ids []int64, inst []installed, tmp string) {
	if len(inst) == 0 {
		fail("inventory needs installed fonts")
	}
	manual := filepath.Join(tmp, "manual-fonts")
	check(os.MkdirAll(manual, 0o755))
	var src string
	for _, f := range inst {
		if f.Kind == "" || f.Kind == "sfnt" {
			src = f.Path
		}
	}
	b, err := os.ReadFile(src)
	check(err)
	check(os.WriteFile(filepath.Join(manual, "copied-by-hand.ttf"), b, 0o644))
	check(os.WriteFile(filepath.Join(manual, "notes.txt"), []byte("not a font"), 0o644))
	saved := e.extra
	e.extra = append(append([]string{}, saved...),
		"TYPEHAUS_INVENTORY_DIRS="+manual+string(os.PathListSeparator)+filepath.Dir(src))
	defer func() { e.extra = saved }()

	out := e.agent("inventory")
	if !strings.Contains(out, "1 font(s) installed outside typehaus") {
		fail("expected exactly the copied font:\n%s", out)
	}
	var lic map[string]int64
	e.api("POST", "/api/licenses", map[string]any{"name": "E2E commercial", "kind": "commercial"}, &lic)
	e.api("PATCH", "/api/fonts", map[string]any{"font_ids": ids, "license_id": lic["id"]}, nil)
	e.agent("inventory", "--send")
	var inv struct {
		Unmanaged int `json:"unmanaged_files"`
		Findings  []struct {
			Family string `json:"family"`
			Copies []struct {
				Path  string `json:"path"`
				Exact bool   `json:"exact"`
			} `json:"copies"`
		} `json:"findings"`
	}
	e.api("GET", "/api/inventory", nil, &inv)
	if inv.Unmanaged != 1 || len(inv.Findings) != 1 || len(inv.Findings[0].Copies) != 1 || !inv.Findings[0].Copies[0].Exact {
		fail("inventory on the server: %+v", inv)
	}
	fmt.Printf("ok: server lists %s as an unmanaged copy (%s)\n", inv.Findings[0].Family, inv.Findings[0].Copies[0].Path)
	e.api("PATCH", "/api/fonts", map[string]any{"font_ids": ids, "license_id": 0}, nil)
	e.api("DELETE", fmt.Sprintf("/api/licenses/%d", lic["id"]), nil, nil)
}

// existing puts a copy of a library font where the system loads fonts from,
// as if the person had installed it themselves, and checks that the app
// reports it instead of installing it again – and installs the library's
// once the other copy is gone.
func (e *env) existing(ids []int64, inst []installed, tmp string) []installed {
	var f *installed
	for i := range inst {
		if inst[i].Kind == "" || inst[i].Kind == "sfnt" {
			f = &inst[i]
		}
	}
	if f == nil {
		fail("existing needs an installed TrueType/OpenType font")
	}
	b, err := os.ReadFile(f.Path)
	check(err)
	e.api("PUT", "/api/settings", map[string]any{"existing_fonts": "skip"}, nil)
	e.api("DELETE", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	if n := len(e.installed(e.home)); n != 0 {
		fail("expected 0 installed fonts, got %d", n)
	}

	own := filepath.Join(tmp, "own-fonts")
	check(os.MkdirAll(own, 0o755))
	copyPath := filepath.Join(own, "installed-by-hand.ttf")
	check(os.WriteFile(copyPath, b, 0o644))
	saved := e.extra
	e.extra = append(append([]string{}, saved...), "TYPEHAUS_INVENTORY_DIRS="+own)
	defer func() { e.extra = saved }()

	e.api("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	now := e.installed(e.home)
	for _, g := range now {
		if g.SHA256 == f.SHA256 {
			fail("%s was installed next to the copy already on the computer", f.Family)
		}
	}
	if len(now) != len(inst)-1 {
		fail("expected the other %d font(s) installed, got %d", len(inst)-1, len(now))
	}
	ex, _ := e.state()["existing"].(map[string]any)
	got, _ := ex[f.SHA256].(map[string]any)
	if got == nil || got["same"] != true || filepath.Clean(got["path"].(string)) != filepath.Clean(copyPath) {
		fail("app state doesn't name the existing copy: %v", e.state()["existing"])
	}
	if c, err := os.ReadFile(copyPath); err != nil || string(c) != string(b) {
		fail("the person's own copy was touched")
	}
	var devs []struct {
		Existing []struct {
			Path string `json:"path"`
			Same bool   `json:"same"`
		} `json:"existing"`
	}
	e.api("GET", "/api/devices?all=1", nil, &devs)
	reported := false
	for _, d := range devs {
		for _, x := range d.Existing {
			reported = reported || (x.Same && filepath.Clean(x.Path) == filepath.Clean(copyPath))
		}
	}
	if !reported {
		fail("the server doesn't list the existing copy: %+v", devs)
	}

	// the copy goes: the next ordinary sync installs the library's own
	check(os.Remove(copyPath))
	e.agent("sync")
	now = e.installed(e.home)
	if len(now) != len(inst) {
		fail("expected %d installed after the copy was removed, got %d", len(inst), len(now))
	}
	e.api("PUT", "/api/settings", map[string]any{"existing_fonts": "install"}, nil)
	return now
}

func (e *env) state() map[string]any {
	b, err := os.ReadFile(filepath.Join(e.home, "state.json"))
	check(err)
	var st map[string]any
	check(json.Unmarshal(b, &st))
	return st
}

func mustExist(p string) {
	if _, err := os.Stat(p); err != nil {
		fail("expected file %s: %v", p, err)
	}
	fmt.Println("ok: file present", p)
}

func mustNotExist(p string) {
	if _, err := os.Stat(p); err == nil {
		fail("file should be gone: %s", p)
	}
	fmt.Println("ok: file removed", p)
}

// pluginFlow runs the app headless (as at login) and acts like an app plugin:
// it asks about a document's fonts, switches the missing ones on and expects
// them installed when the answer comes.
func (e *env) pluginFlow(expected map[string]bool) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(*agentBin, "run")
	cmd.Env = append(append(os.Environ(), e.extra...), fmt.Sprintf("TYPEHAUS_PLUGIN_PORT=%d", port))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	check(cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if failed {
			fmt.Println("---- agent run output ----\n" + out.String())
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	call := func(method, path string, body any, v any) int {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("X-Typehaus-Plugin", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		if v != nil {
			json.NewDecoder(resp.Body).Decode(v)
		}
		return resp.StatusCode
	}
	waitFor("plugin API", 20*time.Second, func() bool {
		var st map[string]any
		return call("GET", "/v1/status", nil, &st) == 200 && st["connected"] == true
	})

	var lib []struct {
		SHA256     string `json:"sha256"`
		PostScript string `json:"postscript_name"`
		Family     string `json:"family"`
		Style      string `json:"style"`
	}
	e.api("GET", "/api/fonts", nil, &lib)
	var doc []map[string]string
	for _, f := range lib {
		doc = append(doc, map[string]string{"postscript_name": f.PostScript, "family": f.Family, "style": f.Style})
	}
	doc = append(doc, map[string]string{"postscript_name": "Futura-Bold", "family": "Futura", "style": "Bold"})
	type fontStatus struct {
		Status    string `json:"status"`
		SHA256    string `json:"sha256"`
		Installed bool   `json:"installed"`
		Reason    string `json:"reason"`
	}
	var chk struct{ Fonts []fontStatus }
	if st := call("POST", "/v1/fonts/check", map[string]any{"fonts": doc, "app": "e2e"}, &chk); st != 200 || len(chk.Fonts) != len(doc) {
		fail("check: %d %+v", st, chk)
	}
	for i, f := range chk.Fonts {
		switch {
		case i == len(doc)-1:
			if f.Status != "not_available" {
				fail("Futura should be not_available: %+v", f)
			}
		case expected[f.SHA256]:
			if f.Status != "available" || f.Installed {
				fail("%s should be available and not installed: %+v", doc[i]["postscript_name"], f)
			}
		default:
			if f.Status != "unsupported" {
				fail("%s isn't for %s – want unsupported: %+v", doc[i]["postscript_name"], runtime.GOOS, f)
			}
		}
	}
	var act struct {
		Fonts    []fontStatus
		Complete bool
	}
	if st := call("POST", "/v1/fonts/activate", map[string]any{"fonts": doc, "app": "e2e"}, &act); st != 200 || !act.Complete {
		fail("activate: %d %+v", st, act)
	}
	n := 0
	for _, f := range act.Fonts {
		if expected[f.SHA256] {
			if f.Status != "active" || !f.Installed {
				fail("after activate: %+v", f)
			}
			n++
		}
	}
	inst := e.installed(e.home)
	if n != len(expected) || len(inst) != len(expected) {
		fail("plugin activation: %d reported, %d installed, want %d", n, len(inst), len(expected))
	}
	for _, f := range inst {
		verifyUserInstalled(e, f)
	}
	fmt.Printf("ok: %d font(s) switched on by a plugin and installed while it waited\n", n)
	if st := call("POST", "/v1/requests", map[string]any{"font": "Futura Bold", "app": "e2e"}, nil); st != 201 {
		fail("request: %d", st)
	}
	// web pages are refused
	req, _ := http.NewRequest("POST", base+"/v1/fonts/check", strings.NewReader(`{"fonts":[]}`))
	req.Header.Set("X-Typehaus-Plugin", "1")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		fail("web origin must be refused")
	} else {
		resp.Body.Close()
	}
}

// affinityFlow runs the app against an emulation of Affinity's MCP server
// whose open document uses the library's fonts: the app must read the
// document, switch the fonts on and install them – nothing else involved.
func (e *env) affinityFlow(expected map[string]bool) {
	fake, err := affinitytest.Start("")
	check(err)
	defer fake.Close()
	var lib []struct {
		SHA256     string `json:"sha256"`
		PostScript string `json:"postscript_name"`
		Kind       string `json:"kind"`
	}
	e.api("GET", "/api/fonts", nil, &lib)
	// a document holds outline fonts (TrueType, OpenType, Type 1) only
	doc := affinitytest.Doc{Title: "E2E poster.af", Current: true, Fonts: []affinitytest.Font{{Name: "Futura-Bold"}, {Name: "ArialMT", Installed: true}}}
	want := map[string]bool{}
	for _, f := range lib {
		if f.PostScript == "" || (f.Kind != "sfnt" && f.Kind != "type1") {
			continue
		}
		doc.Fonts = append(doc.Fonts, affinitytest.Font{Name: f.PostScript})
		if expected[f.SHA256] {
			want[f.SHA256] = true
		}
	}
	if len(want) == 0 {
		fail("no outline font in the library to test Affinity with")
	}
	fake.SetDocs(doc)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(*agentBin, "run")
	cmd.Env = append(append(os.Environ(), e.extra...), fmt.Sprintf("TYPEHAUS_PLUGIN_PORT=%d", port),
		"TYPEHAUS_AFFINITY_URL="+fake.URL, "TYPEHAUS_AFFINITY=auto")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	check(cmd.Start())
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if failed {
			fmt.Println("---- agent run output ----\n" + out.String())
		}
	}()
	waitFor("fonts installed for the Affinity document", 60*time.Second, func() bool {
		n := 0
		for _, f := range e.installed(e.home) {
			if want[f.SHA256] {
				n++
			}
		}
		return n == len(want)
	})
	for _, f := range e.installed(e.home) {
		if !want[f.SHA256] {
			fail("%s isn't used by the document and must not be installed", f.Path)
		}
		verifyUserInstalled(e, f)
	}
	calls := strings.Join(fake.Calls(), ",")
	if !strings.Contains(calls, "initialize") || !strings.Contains(calls, "tools/call:read_sdk_documentation_topic") || fake.Scripts() == 0 {
		fail("unexpected MCP conversation: %s", calls)
	}
	fmt.Printf("ok: %d font(s) an Affinity document uses were switched on and installed (%d scripts run)\n", len(want), fake.Scripts())
}
