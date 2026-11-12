//go:build windows

package tray

import (
	"strconv"
	"sync"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/4nonX/typehaus/internal/brand"
)

// Available: the notification area always exists on Windows.
func Available() bool { return true }

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetSystemMetricsDpi = user32.NewProc("GetSystemMetricsForDpi")
	procGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	iconCache               sync.Map
	smCXSmIcon, smCXIcon    uintptr = 49, 11
)

func metric(idx uintptr) int {
	if procGetSystemMetricsDpi.Find() == nil && procGetDpiForSystem.Find() == nil {
		dpi, _, _ := procGetDpiForSystem.Call()
		if v, _, _ := procGetSystemMetricsDpi.Call(idx, dpi); v > 0 {
			return int(v)
		}
	}
	v, _, _ := procGetSystemMetrics.Call(idx)
	return int(v)
}

// lightTaskbar reports whether the taskbar uses the light theme.
func lightTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}

func iconVariantKey() string {
	return strconv.FormatBool(lightTaskbar()) + strconv.Itoa(metric(smCXSmIcon))
}

// setIcon renders the mark pixel-snapped at the tray's small-icon size and
// hands Windows an exact 2× nearest-neighbour enlargement: the systray
// library loads icons at the large-icon size and the shell scales them down,
// which reproduces the crisp small rendering at every DPI.
func setIcon(s brand.State, frame int) {
	small := metric(smCXSmIcon)
	if small < 16 {
		small = 16
	}
	large := metric(smCXIcon)
	f := large / small
	if f < 1 {
		f = 1
	}
	v := brand.OnDark
	if lightTaskbar() {
		v = brand.OnLight
	}
	key := strconv.Itoa(int(s)) + "/" + strconv.Itoa(frame%2) + "/" + strconv.Itoa(int(v)) + "/" + strconv.Itoa(small) + "/" + strconv.Itoa(f)
	if b, ok := iconCache.Load(key); ok {
		systray.SetIcon(b.([]byte))
		return
	}
	b := brand.ICO(brand.Upscale(brand.Mark(small, v, s, frame), f))
	iconCache.Store(key, b)
	systray.SetIcon(b)
}
