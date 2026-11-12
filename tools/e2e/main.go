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
	"strings"
	"time"
)

var (
	serverBin = flag.String("server", "", "path to typehaus-server")
	agentBin  = flag.String("agent", "", "path to typehaus-agent")
	fontPath  = flag.String("font", "", "TTF/OTF file to use")
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
		"TYPEHAUS_ADMIN_PASSWORD=e2e-password-123", "TYPEHAUS_POLL_SECONDS=10", "TYPEHAUS_TRUST_PROXY=false")
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

	step("sign in and upload " + filepath.Base(*fontPath))
	e.api("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "e2e-password-123"}, nil)
	ids := e.upload(*fontPath)
	var fonts []struct {
		ID     int64  `json:"id"`
		Family string `json:"family"`
	}
	e.api("GET", "/api/fonts", nil, &fonts)
	e.family = fonts[0].Family
	var col map[string]int64
	e.api("POST", "/api/collections", map[string]any{"name": "E2E", "everyone": true}, &col)
	e.api("POST", fmt.Sprintf("/api/collections/%d/fonts", col["id"]), map[string]any{"font_ids": ids}, nil)
	e.api("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)

	step("enroll the agent and install for this user")
	e.agent("enroll", "--server", base, "--code", e.code(), "--name", "e2e")
	inst := e.installed(e.home)
	if len(inst) != 1 {
		fail("expected 1 installed font, got %d", len(inst))
	}
	verifyUserInstalled(e, inst[0])
	out := e.agent("status")
	if !strings.Contains(out, "Installed:  1 font file") {
		fail("status output unexpected:\n%s", out)
	}

	step("deactivate and remove")
	e.api("DELETE", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("sync")
	if n := len(e.installed(e.home)); n != 0 {
		fail("expected 0 installed fonts after deactivation, got %d", n)
	}
	verifyUserRemoved(e, inst[0])

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
	verifyUserRemoved(e, inst[0])

	if *machine {
		step("machine-wide service (all users)")
		testMachine(e, ids)
	}
	fmt.Println("\nE2E PASSED")
}

// ---- helpers -------------------------------------------------------------------

var failed bool

func fail(format string, a ...any) {
	failed = true
	fmt.Printf("FAIL: "+format+"\n", a...)
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

func (e *env) upload(path string) []int64 {
	data, err := os.ReadFile(path)
	check(err)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", filepath.Base(path))
	fw.Write(data)
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
	if len(out.FontIDs) == 0 {
		fail("upload failed: %+v", out.Results)
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
	SHA256   string `json:"sha256"`
	Path     string `json:"path"`
	FullName string `json:"full_name"`
	Family   string `json:"family"`
	RegName  string `json:"reg_name"`
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
