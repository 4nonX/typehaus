package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/adobeplugins"
)

// Installing the typehaus panels for Adobe apps. InDesign and Photoshop
// panels (.ccx) are built from the copy in this app, so they always match
// it; the Illustrator extension (.zxp) must be signed and comes from the
// server (the release build signs it). Adobe's Unified Plugin Installer
// Agent (part of the Creative Cloud app) installs them; without it the
// file is opened, which hands it to Creative Cloud too.

// AdobePluginInfo is what the typehaus window shows.
type AdobePluginInfo struct {
	ID  string `json:"id"`
	App string `json:"app"`
}

// AdobePlugins lists the panels for this system (none on Linux).
func AdobePlugins() []AdobePluginInfo {
	out := []AdobePluginInfo{}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return out
	}
	for _, p := range adobeplugins.Plugins {
		out = append(out, AdobePluginInfo{p.ID, p.App})
	}
	return out
}

// upiaPath finds Adobe's plugin installer.
func upiaPath() string {
	if p := os.Getenv("TYPEHAUS_UPIA"); p != "" { // tests
		return p
	}
	var p string
	switch runtime.GOOS {
	case "windows":
		cf := os.Getenv("CommonProgramFiles")
		if cf == "" {
			cf = `C:\Program Files\Common Files`
		}
		p = filepath.Join(cf, "Adobe", "Adobe Desktop Common", "RemoteComponents", "UPI", "UnifiedPluginInstallerAgent", "UnifiedPluginInstallerAgent.exe")
	case "darwin":
		p = "/Library/Application Support/Adobe/Adobe Desktop Common/RemoteComponents/UPI/UnifiedPluginInstallerAgent/UnifiedPluginInstallerAgent.app/Contents/MacOS/UnifiedPluginInstallerAgent"
	}
	if p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// InstallAdobePlugin installs one panel and says what happened.
func (c *Controller) InstallAdobePlugin(ctx context.Context, id string) (string, error) {
	var plug *adobeplugins.Plugin
	for i := range adobeplugins.Plugins {
		if adobeplugins.Plugins[i].ID == id {
			plug = &adobeplugins.Plugins[i]
		}
	}
	if plug == nil {
		return "", errors.New("unknown plugin")
	}
	var data []byte
	var err error
	if plug.Format == "ccx" {
		data, err = adobeplugins.CCX(plug.ID, Version)
	} else {
		data, err = c.downloadPlugin(ctx, plug.File)
	}
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "typehaus-plugin-")
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, plug.File)
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return "", err
	}
	// left for the installer that may still read it; the system cleans temp
	if upia := upiaPath(); upia != "" {
		arg := "--install"
		if runtime.GOOS == "windows" {
			arg = "/install"
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, upia, arg, file)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		if err != nil {
			c.log.Warn("Adobe plugin installer failed", "plugin", id, "err", err, "output", msg)
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("Adobe's installer couldn't install the %s panel: %s", plug.App, lastLine(msg))
		}
		c.addEvent("info", "Installed the typehaus panel for "+plug.App)
		c.notify()
		return fmt.Sprintf("Installed. Restart %s, then open %s.", plug.App, panelMenu(plug)), nil
	}
	if err := OpenURL(file); err != nil {
		return "", errors.New("Creative Cloud's plugin installer wasn't found – is the Creative Cloud app installed?")
	}
	return fmt.Sprintf("Creative Cloud takes over – confirm the installation there, then restart %s.", plug.App), nil
}

func panelMenu(p *adobeplugins.Plugin) string {
	if p.Format == "zxp" {
		return "Window → Extensions (Legacy) → typehaus"
	}
	return "Plugins → typehaus"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (c *Controller) downloadPlugin(ctx context.Context, name string) ([]byte, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if cfg == nil {
		return nil, errors.New("connect typehaus first – the Illustrator panel comes from your typehaus server")
	}
	hc, err := NewHTTPClient(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	req, err := httpRequest(ctx, cfg.Server+"/downloads/"+name)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("your typehaus server doesn't offer the Illustrator panel – it comes with the release builds")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 50<<20))
}

func httpRequest(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	return req, nil
}
