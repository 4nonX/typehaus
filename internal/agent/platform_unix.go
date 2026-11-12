//go:build !windows && !darwin

package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

func (p *unixPlatform) install(*Installed) error { return nil }

func (p *unixPlatform) uninstall(f *Installed) error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *unixPlatform) refresh() {
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
