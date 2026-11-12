//go:build !windows && !darwin

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/4nonX/typehaus/internal/brand"
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

func unitPath(cfg string) string {
	return filepath.Join(cfg, "systemd", "user", "typehaus-agent.service")
}
func desktopPath(cfg string) string {
	return filepath.Join(cfg, "autostart", "typehaus-agent.desktop")
}

// EnableAutostart starts the agent at login. Tray mode uses an XDG autostart
// entry (it needs the desktop session); headless mode prefers a systemd user
// service and falls back to XDG autostart.
func EnableAutostart(mode string, startNow bool) (string, error) {
	if startNow {
		StopRunning()
	}
	bin, err := installBinary()
	if err != nil {
		return "", err
	}
	cfg, err := xdgConfig()
	if err != nil {
		return "", err
	}
	// only one mechanism at a time
	os.Remove(desktopPath(cfg))
	if _, err := os.Stat(unitPath(cfg)); err == nil {
		exec.Command("systemctl", "--user", "disable", "typehaus-agent.service").Run()
		os.Remove(unitPath(cfg))
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	}

	if mode == "run" && hasSystemdUser() {
		unit := unitPath(cfg)
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
		args := []string{"--user", "enable", "typehaus-agent.service"}
		if startNow {
			args = append(args, "--now")
		}
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl enable: %v: %s", err, out)
		}
		return "systemd user service " + unit, nil
	}

	installDesktopIcon()
	desk := desktopPath(cfg)
	content := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=typehaus\nComment=Keeps your typehaus fonts in sync\nExec=%s %s\nIcon=typehaus\nTerminal=false\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n",
		quoteArg(bin), autostartArgs(mode))
	if err := os.MkdirAll(filepath.Dir(desk), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(desk, []byte(content), 0o644); err != nil {
		return "", err
	}
	if startNow {
		cmd := exec.Command(bin, mode)
		if err := cmd.Start(); err == nil {
			cmd.Process.Release()
		}
	}
	return "XDG autostart entry " + desk, nil
}

// installDesktopIcon puts the app icon into the user's icon theme so the
// autostart entry and notifications show the typehaus mark.
func installDesktopIcon() {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		base = filepath.Join(home, ".local", "share")
	}
	for _, sz := range []int{48, 256} {
		d := filepath.Join(base, "icons", "hicolor", fmt.Sprintf("%dx%d", sz, sz), "apps")
		if os.MkdirAll(d, 0o755) == nil {
			os.WriteFile(filepath.Join(d, "typehaus.png"), brand.PNG(brand.Mark(sz, brand.Tile, brand.StateOK, 0)), 0o644)
		}
	}
}

// DisableAutostart removes the service / autostart entry.
func DisableAutostart(stop bool) error {
	cfg, err := xdgConfig()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath(cfg)); err == nil {
		args := []string{"--user", "disable", "typehaus-agent.service"}
		if stop {
			args = append(args, "--now")
		}
		exec.Command("systemctl", args...).Run()
	}
	for _, p := range []string{unitPath(cfg), desktopPath(cfg)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	if stop {
		StopRunning()
	}
	return nil
}

// InstallNotice tells the user how to fix an installation problem, if any.
func InstallNotice() string { return "" }

// AutostartEnabled reports whether an autostart entry exists.
func AutostartEnabled() bool {
	cfg, err := xdgConfig()
	if err != nil {
		return false
	}
	for _, p := range []string{unitPath(cfg), desktopPath(cfg)} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
