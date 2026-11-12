//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const fontsKey = `Software\Microsoft\Windows NT\CurrentVersion\Fonts`

func regValue(root registry.Key, name string) (string, bool) {
	k, err := registry.OpenKey(root, fontsKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err == nil
}

// gdiFamilies lists the font families GDI applications see in this session.
func gdiFamilies() string {
	out, _ := exec.Command("powershell", "-NoProfile", "-Command",
		"Add-Type -AssemblyName System.Drawing; (New-Object System.Drawing.Text.InstalledFontCollection).Families | ForEach-Object { $_.Name }").CombinedOutput()
	return string(out)
}

func verifyUserInstalled(e *env, f installed) {
	mustExist(f.Path)
	v, ok := regValue(registry.CURRENT_USER, f.RegName)
	if !ok || !strings.EqualFold(v, f.Path) {
		fail("HKCU font value %q missing or wrong (%q)", f.RegName, v)
	}
	fmt.Println("ok: HKCU registration", f.RegName)
	if !strings.Contains(gdiFamilies(), e.family) {
		fail("GDI does not list family %q after per-user install", e.family)
	}
	fmt.Println("ok: applications see family", e.family)
}

func verifyUserRemoved(e *env, f installed) {
	mustNotExist(f.Path)
	if _, ok := regValue(registry.CURRENT_USER, f.RegName); ok {
		fail("HKCU font value %q still present", f.RegName)
	}
	fmt.Println("ok: HKCU registration removed")
}

func testMachine(e *env, ids []int64) {
	machineDir := filepath.Join(os.Getenv("ProgramData"), "typehaus", "machine")
	e.api("POST", "/api/activations", map[string]any{"font_ids": ids}, nil)
	e.agent("machine", "enroll", "--server", e.base, "--code", e.code())
	e.agent("machine", "install")
	var f installed
	waitFor("machine-wide install", 90*time.Second, func() bool {
		inst := e.installed(machineDir)
		if len(inst) == 1 {
			f = inst[0]
			return true
		}
		return false
	})
	e.agent("machine", "status")
	mustExist(f.Path)
	if !strings.EqualFold(filepath.Dir(f.Path), filepath.Join(os.Getenv("WINDIR"), "Fonts")) {
		fail("machine font not in %%WINDIR%%\\Fonts: %s", f.Path)
	}
	v, ok := regValue(registry.LOCAL_MACHINE, f.RegName)
	if !ok || !strings.EqualFold(v, filepath.Base(f.Path)) {
		fail("HKLM font value %q missing or wrong (%q)", f.RegName, v)
	}
	fmt.Println("ok: HKLM registration", f.RegName)
	pub, err := os.ReadFile(filepath.Join(os.Getenv("ProgramData"), "typehaus", "public", "fonts.json"))
	if err != nil || !strings.Contains(string(pub), f.SHA256) {
		fail("public machine font list missing the font: %v", err)
	}
	fmt.Println("ok: public machine font list")

	// A per-user agent on the same PC must not install the font a second time.
	e.agent("enroll", "--server", e.base, "--code", e.code(), "--name", "e2e-user")
	if n := len(e.installed(e.home)); n != 0 {
		fail("per-user agent installed %d font(s) although the machine already has them", n)
	}
	fmt.Println("ok: per-user agent skips machine-wide fonts")
	e.agent("unenroll")

	e.agent("machine", "uninstall")
	mustNotExist(f.Path)
	if _, ok := regValue(registry.LOCAL_MACHINE, f.RegName); ok {
		fail("HKLM font value still present after uninstall")
	}
	out := e.agent("machine", "status")
	if !strings.Contains(out, "not installed") {
		fail("service should be gone:\n%s", out)
	}
	fmt.Println("ok: machine mode uninstalled cleanly")
}
