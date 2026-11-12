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

func newPlatform(log *slog.Logger) (platform, error) {
	if machineMode {
		return &machinePlatform{log: log}, nil
	}
	return &winPlatform{log: log}, nil
}

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

// winResource is the string AddFontResourceW expects: the file, or for
// PostScript Type 1 "metrics.pfm|outlines.pfb".
func winResource(f *Installed) (string, error) {
	switch f.Kind {
	case "type1":
		pfm := extraWithExt(f, ".pfm")
		if pfm == "" {
			return "", errors.New("PostScript Type 1 fonts need their .pfm metrics file on Windows")
		}
		return pfm + "|" + f.Path, nil
	case "pcf", "bdf":
		return "", errors.New("X11 bitmap fonts can't be installed on Windows")
	}
	return f.Path, nil
}

func addFontResource(res string) bool {
	ptr, err := windows.UTF16PtrFromString(res)
	if err != nil {
		return false
	}
	r, _, _ := procAddFontResourceW.Call(uintptr(unsafe.Pointer(ptr)))
	return r != 0
}

func removeFontResource(res string) {
	ptr, err := windows.UTF16PtrFromString(res)
	if err != nil {
		return
	}
	// a resource may have been added more than once; remove all references
	for i := 0; i < 8; i++ {
		if r, _, _ := procRemoveFontResourceW.Call(uintptr(unsafe.Pointer(ptr))); r == 0 {
			break
		}
	}
}

func (p *winPlatform) install(f *Installed) error {
	res, err := winResource(f)
	if err != nil {
		return err
	}
	if !addFontResource(res) {
		return fmt.Errorf("Windows rejected %s (unsupported or corrupt font)", filepath.Base(f.Path))
	}
	if f.Kind == "type1" {
		return nil // no per-user registry for Type 1: re-added at every login (ReloadSession)
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
	if res, err := winResource(f); err == nil {
		removeFontResource(res)
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
	removeExtras(f)
	return nil
}

// reload: per-user registration at logon covers TrueType/OpenType; bitmap
// and Type 1 fonts are added to the session again by the agent.
func (p *winPlatform) reload(f *Installed) bool {
	if f.Kind != "type1" && f.Kind != "winfnt" {
		return false
	}
	res, err := winResource(f)
	return err == nil && addFontResource(res)
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
