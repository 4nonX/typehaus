//go:build !windows && !darwin

package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Linux / BSD: fontconfig scans $XDG_DATA_HOME/fonts recursively. We keep our
// files in a dedicated "typehaus" subfolder and refresh the cache with fc-cache.

type unixPlatform struct{ log *slog.Logger }

func newPlatform(log *slog.Logger) (platform, error) { return &unixPlatform{log: log}, nil }

func (p *unixPlatform) fontDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "fonts", "typehaus"), nil
}

func (p *unixPlatform) filePrefix() string { return "" }

// install: fontconfig/FreeType read every supported kind (TrueType/OpenType,
// Type 1 with .afm, PCF, BDF, Windows .fon/.fnt) straight from the folder.
func (p *unixPlatform) install(*Installed) error { return nil }

func (p *unixPlatform) uninstall(f *Installed) error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	removeExtras(f)
	return nil
}

func (p *unixPlatform) reload(*Installed) bool { return false }

// acceptTypehausFonts adds a per-user fontconfig rule that always accepts
// fonts in the typehaus folder. Many distributions reject non-scalable
// bitmap fonts (PCF, BDF, Windows .fon) by default (70-no-bitmaps.conf) –
// but a font the user switched on in typehaus should be usable.
func (p *unixPlatform) acceptTypehausFonts() {
	dir, err := p.fontDir()
	if err != nil {
		return
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		cfg = filepath.Join(home, ".config")
	}
	conf := filepath.Join(cfg, "fontconfig", "conf.d", "50-typehaus.conf")
	content := `<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<!-- Written by typehaus: fonts you activate in typehaus are always usable,
     including bitmap fonts that the system configuration would hide. -->
<fontconfig>
  <selectfont>
    <acceptfont>
      <glob>` + xmlEscape(dir) + `/*</glob>
    </acceptfont>
  </selectfont>
</fontconfig>
`
	if b, err := os.ReadFile(conf); err == nil && string(b) == content {
		return
	}
	if os.MkdirAll(filepath.Dir(conf), 0o755) == nil {
		os.WriteFile(conf, []byte(content), 0o644)
	}
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (p *unixPlatform) refresh() {
	p.acceptTypehausFonts()
	fc, err := exec.LookPath("fc-cache")
	if err != nil {
		p.log.Debug("fc-cache not found; applications may need a restart to see font changes")
		return
	}
	dir, err := p.fontDir()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, fc, "-f", dir).CombinedOutput(); err != nil {
		p.log.Warn("fc-cache failed", "err", err, "output", string(out))
	}
}
