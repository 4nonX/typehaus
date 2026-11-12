//go:build darwin

package agent

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
)

// macOS activates fonts placed in ~/Library/Fonts for the current user;
// fontd picks up changes automatically. Files carry a "typehaus-" prefix so
// they are recognisable and never collide with fonts the user installed.

type darwinPlatform struct{ log *slog.Logger }

func newPlatform(log *slog.Logger) (platform, error) { return &darwinPlatform{log: log}, nil }

func (p *darwinPlatform) fontDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Fonts"), nil
}

func (p *darwinPlatform) filePrefix() string { return "typehaus-" }

func (p *darwinPlatform) install(*Installed) error { return nil }

func (p *darwinPlatform) uninstall(f *Installed) error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *darwinPlatform) refresh() {}
