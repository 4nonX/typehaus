//go:build !windows && !darwin

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func xdgConfig() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

func hasSystemdUser() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "show-environment").Run() == nil
}

// quoteArg quotes a path for systemd ExecStart / desktop Exec lines.
func quoteArg(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// EnableAutostart installs a systemd user service, or an XDG autostart entry
// when systemd --user is unavailable.
func EnableAutostart() (string, error) {
	StopRunning()
	bin, err := installBinary()
	if err != nil {
		return "", err
	}
	cfg, err := xdgConfig()
	if err != nil {
		return "", err
	}
	if hasSystemdUser() {
		unit := filepath.Join(cfg, "systemd", "user", "typehaus-agent.service")
		content := fmt.Sprintf(`[Unit]
Description=typehaus font sync agent
After=graphical-session.target network-online.target

[Service]
ExecStart=%s run
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`, quoteArg(bin))
		if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(unit, []byte(content), 0o644); err != nil {
			return "", err
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "typehaus-agent.service").CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl enable: %v: %s", err, out)
		}
		return "installed and started systemd user service " + unit, nil
	}
	desk := filepath.Join(cfg, "autostart", "typehaus-agent.desktop")
	content := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=typehaus agent\nExec=%s run\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n", quoteArg(bin))
	if err := os.MkdirAll(filepath.Dir(desk), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(desk, []byte(content), 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "run")
	if err := cmd.Start(); err == nil {
		cmd.Process.Release()
	}
	return "installed XDG autostart entry " + desk + " and started the agent", nil
}

// DisableAutostart removes the service / autostart entry and stops the agent.
func DisableAutostart() error {
	cfg, err := xdgConfig()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "--user", "disable", "--now", "typehaus-agent.service").Run()
	}
	for _, p := range []string{
		filepath.Join(cfg, "systemd", "user", "typehaus-agent.service"),
		filepath.Join(cfg, "autostart", "typehaus-agent.desktop"),
	} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	StopRunning()
	return nil
}
