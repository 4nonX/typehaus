//go:build darwin && cgo

package tray

import (
	"fyne.io/systray"

	"github.com/4nonX/typehaus/internal/brand"
)

// Available: the menu bar always exists on macOS.
func Available() bool { return true }

func iconVariantKey() string { return "" }

// setIcon uses a template image (black + alpha) rendered at 32 px for the
// 16 pt menu-bar slot, so macOS tints it for light/dark menu bars and Retina
// displays get a pixel-exact 2× rendering.
func setIcon(s brand.State, frame int) {
	tmpl := brand.PNG(brand.Mark(32, brand.Template, s, frame))
	systray.SetTemplateIcon(tmpl, tmpl)
}
