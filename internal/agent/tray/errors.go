package tray

import "errors"

// ErrUnavailable means no tray/menu-bar host is available; callers fall
// back to running headless.
var ErrUnavailable = errors.New("no system tray available")
