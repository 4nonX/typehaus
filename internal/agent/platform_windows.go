//go:build windows

package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Per-user font installation (Windows 10 1809+): the font file lives in a
// user-writable folder, is registered under HKCU so it loads at every logon,
// and is added to the running session with AddFontResourceW. No admin rights.

const userFontsKey = `Software\Microsoft\Windows NT\CurrentVersion\Fonts`

var (
	gdi32                   = windows.NewLazySystemDLL("gdi32.dll")
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procAddFontResourceW    = gdi32.NewProc("AddFontResourceW")
	procRemoveFontResourceW = gdi32.NewProc("RemoveFontResourceW")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
)

type winPlatform struct{ log *slog.Logger }

func newPlatform(log *slog.Logger) (platform, error) { return &winPlatform{log: log}, nil }

func (p *winPlatform) fontDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		var err error
		if base, err = os.UserCacheDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "typehaus", "fonts"), nil
}

func (p *winPlatform) filePrefix() string { return "" }

func (p *winPlatform) install(f *Installed) error {
	ptr, err := windows.UTF16PtrFromString(f.Path)
	if err != nil {
		return err
	}
	if r, _, _ := procAddFontResourceW.Call(uintptr(unsafe.Pointer(ptr))); r == 0 {
		return fmt.Errorf("AddFontResourceW rejected %s (unsupported or corrupt font)", filepath.Base(f.Path))
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, userFontsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU font key: %w", err)
	}
	defer k.Close()
	// Suffix avoids clobbering a font the user installed manually under the same name.
	f.RegName = f.FullName + " [typehaus " + f.SHA256[:8] + "]"
	return k.SetStringValue(f.RegName, f.Path)
}

func (p *winPlatform) uninstall(f *Installed) error {
	if ptr, err := windows.UTF16PtrFromString(f.Path); err == nil {
		// A resource may have been added more than once; remove all references.
		for i := 0; i < 8; i++ {
			if r, _, _ := procRemoveFontResourceW.Call(uintptr(unsafe.Pointer(ptr))); r == 0 {
				break
			}
		}
	}
	if f.RegName != "" {
		if k, err := registry.OpenKey(registry.CURRENT_USER, userFontsKey, registry.SET_VALUE); err == nil {
			if err := k.DeleteValue(f.RegName); err != nil && !errors.Is(err, registry.ErrNotExist) {
				p.log.Warn("delete registry value", "name", f.RegName, "err", err)
			}
			k.Close()
		}
	}
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p *winPlatform) refresh() {
	const (
		hwndBroadcast   = 0xffff
		wmFontChange    = 0x001D
		smtoAbortIfHung = 0x0002
	)
	var result uintptr
	procSendMessageTimeoutW.Call(hwndBroadcast, wmFontChange, 0, 0, smtoAbortIfHung, 2000, uintptr(unsafe.Pointer(&result)))
}
