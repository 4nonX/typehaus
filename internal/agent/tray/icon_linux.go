//go:build linux

package tray

import (
	"github.com/godbus/dbus/v5"

	"fyne.io/systray"

	"github.com/4nonX/typehaus/internal/brand"
)

// Available reports whether a StatusNotifierItem host is running (KDE,
// Xfce, Cinnamon, GNOME with the AppIndicator extension, …).
func Available() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func iconVariantKey() string { return "" }

// setIcon sends the self-contained tile; panels scale it to their size and
// it stays legible on both light and dark panels.
//
// The systray library converts semi-transparent pixels incorrectly on Linux
// (it truncates 16-bit premultiplied values), which shows as coloured
// fringes. We therefore send hard-edged alpha; the panel's own downscaling
// smooths the rounded corners again.
func setIcon(s brand.State, frame int) {
	img := brand.Mark(64, brand.Tile, s, frame)
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] < 128 {
			img.Pix[i] = 0
		} else {
			img.Pix[i] = 255
		}
	}
	systray.SetIcon(brand.PNG(img))
}
