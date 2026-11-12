//go:build darwin

package agent

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchLabel = "io.github.typehaus.agent"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

// appBundle returns the enclosing typehaus.app path if the agent runs from
// a bundle, so autostart can launch the app (keeps it out of the Dock and
// gives it its icon) instead of a bare copy of the binary.
func appBundle() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	self, _ = filepath.EvalSymlinks(self)
	if i := strings.Index(self, ".app/Contents/MacOS/"); i > 0 {
		return self[:i+4]
	}
	return ""
}

func plistArgs(mode string) string {
	out := ""
	for _, a := range strings.Fields(autostartArgs(mode)) {
		out += "<string>" + html.EscapeString(a) + "</string>"
	}
	return out
}

// EnableAutostart installs a per-user LaunchAgent. mode is "tray" or "run".
func EnableAutostart(mode string, startNow bool) (string, error) {
	if startNow {
		StopRunning()
	}
	var bin string
	if app := appBundle(); app != "" && !strings.HasPrefix(app, "/Volumes/") {
		// running from an installed typehaus.app: launch it in place
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		bin, _ = filepath.EvalSymlinks(self)
	} else {
		var err error
		if bin, err = installBinary(); err != nil {
			return "", err
		}
	}
	logp, _ := LogPath()
	p, err := plistPath()
	if err != nil {
		return "", err
	}
	processType := "Interactive"
	if mode == "run" {
		processType = "Background"
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string>%s</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>30</integer>
	<key>ProcessType</key><string>%s</string>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, html.EscapeString(bin), plistArgs(mode), processType, html.EscapeString(logp+".stderr"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return "", err
	}
	if startNow {
		exec.Command("launchctl", "unload", p).Run()
		if out, err := exec.Command("launchctl", "load", "-w", p).CombinedOutput(); err != nil {
			return "", fmt.Errorf("launchctl load: %v: %s", err, out)
		}
	}
	return "LaunchAgent " + p + " starts " + bin, nil
}

// DisableAutostart removes the LaunchAgent.
func DisableAutostart(stop bool) error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	if stop {
		exec.Command("launchctl", "unload", "-w", p).Run()
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if stop {
		StopRunning()
	}
	return nil
}

// AutostartEnabled reports whether the LaunchAgent exists.
func AutostartEnabled() bool {
	p, err := plistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
