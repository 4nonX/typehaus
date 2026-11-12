//go:build darwin

package agent

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
)

const launchLabel = "io.github.typehaus.agent"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

// EnableAutostart installs a per-user LaunchAgent and loads it.
func EnableAutostart() (string, error) {
	StopRunning()
	bin, err := installBinary()
	if err != nil {
		return "", err
	}
	logp, _ := LogPath()
	p, err := plistPath()
	if err != nil {
		return "", err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>run</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>30</integer>
	<key>ProcessType</key><string>Background</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, html.EscapeString(bin), html.EscapeString(logp+".stderr"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return "", err
	}
	exec.Command("launchctl", "unload", p).Run()
	if out, err := exec.Command("launchctl", "load", "-w", p).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl load: %v: %s", err, out)
	}
	return "installed LaunchAgent " + p + " running " + bin, nil
}

// DisableAutostart unloads and removes the LaunchAgent.
func DisableAutostart() error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	exec.Command("launchctl", "unload", "-w", p).Run()
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	StopRunning()
	return nil
}
