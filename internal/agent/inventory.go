package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/fontmeta"
	"github.com/4nonX/typehaus/internal/protocol"
)

// Inventory scan: when the server asks for it (an opt-in server setting), the
// agent reports the fonts installed on this computer outside typehaus, at
// most once a day. Only names, sizes and hashes are sent – never the files.

const (
	inventoryEvery   = 24 * time.Hour
	inventoryMaxFile = 64 << 20
)

type inventoryDir struct {
	path  string
	scope string // user | computer
}

// inventoryDirs are the places operating systems load fonts from. Fonts that
// ship with the OS itself (macOS /System) are left out.
func inventoryDirs() []inventoryDir {
	var out []inventoryDir
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		out = append(out, inventoryDir{filepath.Join(win, "Fonts"), "computer"})
		if !machineMode {
			if la := os.Getenv("LOCALAPPDATA"); la != "" {
				out = append(out, inventoryDir{filepath.Join(la, "Microsoft", "Windows", "Fonts"), "user"})
			}
		}
	case "darwin":
		out = append(out, inventoryDir{"/Library/Fonts", "computer"})
		if home != "" {
			out = append(out, inventoryDir{filepath.Join(home, "Library", "Fonts"), "user"})
		}
	default:
		out = append(out, inventoryDir{"/usr/share/fonts", "computer"}, inventoryDir{"/usr/local/share/fonts", "computer"})
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" && home != "" {
			data = filepath.Join(home, ".local", "share")
		}
		if data != "" {
			out = append(out, inventoryDir{filepath.Join(data, "fonts"), "user"})
		}
		if home != "" {
			out = append(out, inventoryDir{filepath.Join(home, ".fonts"), "user"})
		}
	}
	if d := os.Getenv("TYPEHAUS_INVENTORY_DIRS"); d != "" { // tests
		out = nil
		for _, p := range filepath.SplitList(d) {
			out = append(out, inventoryDir{p, "user"})
		}
	}
	return out
}

// inventoryCacheEntry remembers what a file contained, so unchanged files
// are not read and hashed again.
type inventoryCacheEntry struct {
	Size  int64                    `json:"size"`
	MTime int64                    `json:"mtime"`
	Fonts []protocol.InventoryFont `json:"fonts"`
}

func inventoryCachePath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "inventory-cache.json"), err
}

// Inventory scans the font folders and returns every font outside typehaus.
func (a *Agent) Inventory(ctx context.Context) ([]protocol.InventoryFont, error) {
	st, err := LoadState()
	if err != nil {
		return nil, err
	}
	// files typehaus installed itself. Matched by path, not content: a
	// hand-installed copy of a typehaus font is exactly what to report.
	own := map[string]bool{}
	for _, f := range st.Fonts {
		own[filepath.Clean(f.Path)] = true
		for _, e := range f.Extras {
			own[filepath.Clean(e)] = true
		}
	}
	for _, p := range MachineFontPaths() {
		own[filepath.Clean(p)] = true
	}

	cachePath, _ := inventoryCachePath()
	cache := map[string]inventoryCacheEntry{}
	if b, err := os.ReadFile(cachePath); err == nil {
		json.Unmarshal(b, &cache)
	}
	next := map[string]inventoryCacheEntry{}
	out := []protocol.InventoryFont{}
	seen := map[string]bool{}
	for _, dir := range inventoryDirs() {
		filepath.WalkDir(dir.path, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			p = filepath.Clean(p)
			if seen[p] || own[p] || !d.Type().IsRegular() {
				return nil
			}
			seen[p] = true
			info, err := d.Info()
			if err != nil || info.Size() < 4 || info.Size() > inventoryMaxFile {
				return nil
			}
			if fontmeta.CompanionExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			e, ok := cache[p]
			if !ok || e.Size != info.Size() || e.MTime != info.ModTime().UnixNano() {
				e = inventoryCacheEntry{Size: info.Size(), MTime: info.ModTime().UnixNano(), Fonts: scanFontFile(p)}
			}
			next[p] = e
			for _, f := range e.Fonts {
				f.Path, f.Scope = p, dir.scope
				out = append(out, f)
			}
			return nil
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cachePath != "" {
		writeJSONFile(cachePath, next, 0o600)
	}
	return out, nil
}

// scanFontFile reads a file and returns one entry per face (nil if it is not
// a font).
func scanFontFile(p string) []protocol.InventoryFont {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	files, err := fontmeta.Decode(filepath.Base(p), b)
	if err != nil {
		return []protocol.InventoryFont{}
	}
	var out []protocol.InventoryFont
	for _, f := range files {
		for _, face := range f.Faces {
			out = append(out, protocol.InventoryFont{Family: face.Family, Style: face.Style, FullName: face.FullName,
				PostScript: face.PostScriptName, Kind: string(f.Kind), SHA256: sha, Size: int64(len(b))})
		}
	}
	if out == nil {
		out = []protocol.InventoryFont{}
	}
	return out
}

// ErrInventoryOff: the server does not collect inventories.
var ErrInventoryOff = errors.New("the inventory scan is switched off on the server")

// SendInventory scans and reports the fonts installed outside typehaus.
func (a *Agent) SendInventory(ctx context.Context) (int, error) {
	fonts, err := a.Inventory(ctx)
	if err != nil {
		return 0, err
	}
	b, _ := json.Marshal(protocol.InventoryReport{Fonts: fonts})
	resp, err := a.do(ctx, "POST", "/api/agent/inventory", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		return 0, ErrInventoryOff
	}
	if resp.StatusCode != 200 {
		return 0, errors.New(apiError(resp))
	}
	if st, err := LoadState(); err == nil {
		st.InventoryAt = time.Now()
		saveState(st)
	}
	return len(fonts), nil
}

// MaybeInventory sends the inventory when the server asks for it and the
// last one is older than a day.
func (a *Agent) MaybeInventory(ctx context.Context) {
	st, err := LoadState()
	if err != nil || !st.Inventory {
		return
	}
	if age := time.Since(st.InventoryAt); age >= 0 && age < inventoryEvery {
		return
	}
	if n, err := a.SendInventory(ctx); err != nil {
		a.log.Warn("inventory scan failed", "err", err)
	} else {
		a.log.Info("inventory sent", "fonts", n)
	}
}
