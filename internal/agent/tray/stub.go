//go:build !(windows || linux || (darwin && cgo))

package tray

import (
	"log/slog"

	"github.com/4nonX/typehaus/internal/agent"
	"github.com/4nonX/typehaus/internal/agent/ui"
)

// Options configure the tray.
type Options struct {
	OpenWindowIfDisconnected bool
}

// Available is false in builds without tray support (e.g. macOS built
// without cgo, BSDs).
func Available() bool { return false }

// Run is unavailable in this build.
func Run(*agent.Controller, *ui.Server, *slog.Logger, Options) error { return ErrUnavailable }
