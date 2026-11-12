package tray

import (
	"errors"
	"time"
)

// ErrUnavailable means no tray/menu-bar host is available; callers fall
// back to running headless.
var ErrUnavailable = errors.New("no system tray available")

// WaitAvailable polls Available until it is true or the timeout expires.
// At login, the agent can start before the desktop's panel has registered
// its tray host.
func WaitAvailable(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if Available() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}
