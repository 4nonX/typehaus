package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeUPIA writes a script that stands in for Adobe's installer: it keeps
// the file it was given and answers like the real one.
func fakeUPIA(t *testing.T, fail bool) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "got")
	script := "#!/bin/sh\necho \"$1\" > " + got + ".arg\ncp \"$2\" " + got + "\n"
	if fail {
		script += "echo 'Failed to install, status = -410!'\nexit 1\n"
	} else {
		script += "echo 'Installation Successful'\n"
	}
	p := filepath.Join(dir, "upia")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, got
}

func TestInstallAdobePlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TYPEHAUS_AGENT_HOME", home)
	upia, got := fakeUPIA(t, false)
	t.Setenv("TYPEHAUS_UPIA", upia)
	zxp := []byte("PK signed extension")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/downloads/typehaus-illustrator.zxp" {
			w.Write(zxp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := NewController(slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.cfg = &Config{Server: srv.URL, Token: "t"}

	// InDesign: built from the app's own copy and handed to the installer
	msg, err := c.InstallAdobePlugin(context.Background(), "indesign")
	if err != nil || !strings.Contains(msg, "Restart InDesign") {
		t.Fatalf("%q %v", msg, err)
	}
	b, _ := os.ReadFile(got)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("the installer didn't get a .ccx: %v", err)
	}
	found := false
	for _, f := range zr.File {
		found = found || f.Name == "manifest.json"
	}
	if !found {
		t.Fatal("no manifest.json in the package")
	}
	if arg, _ := os.ReadFile(got + ".arg"); strings.TrimSpace(string(arg)) != "--install" {
		t.Fatalf("installer argument %q", arg)
	}
	// Illustrator: the signed package from the server
	if _, err := c.InstallAdobePlugin(context.Background(), "illustrator"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); !bytes.Equal(b, zxp) {
		t.Fatal("the server's .zxp wasn't installed")
	}
	// the installer refuses
	upia2, _ := fakeUPIA(t, true)
	t.Setenv("TYPEHAUS_UPIA", upia2)
	if _, err := c.InstallAdobePlugin(context.Background(), "photoshop"); err == nil || !strings.Contains(err.Error(), "-410") {
		t.Fatalf("installer failure not reported: %v", err)
	}
	// no signed package on the server
	srv.Config.Handler = http.NotFoundHandler()
	if _, err := c.InstallAdobePlugin(context.Background(), "illustrator"); err == nil || !strings.Contains(err.Error(), "release builds") {
		t.Fatalf("missing .zxp: %v", err)
	}
	if _, err := c.InstallAdobePlugin(context.Background(), "premiere"); err == nil {
		t.Fatal("unknown plugin accepted")
	}
}
