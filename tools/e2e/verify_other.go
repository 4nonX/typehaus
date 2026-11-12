//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func verifyUserInstalled(e *env, f installed) {
	mustExist(f.Path)
	for _, x := range f.Extras {
		mustExist(x)
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		if filepath.Dir(f.Path) != filepath.Join(home, "Library", "Fonts") {
			fail("font not in ~/Library/Fonts: %s", f.Path)
		}
		fmt.Println("ok: installed into ~/Library/Fonts")
		// CoreText lists fonts registered for the user; give fontd a moment.
		out, _ := exec.Command("system_profiler", "SPFontsDataType").CombinedOutput()
		if strings.Contains(string(out), filepath.Base(f.Path)) {
			fmt.Println("ok: macOS font registry lists", filepath.Base(f.Path))
		} else {
			fmt.Println("note: system_profiler did not list the font yet (fontd indexes asynchronously)")
		}
	default:
		out, err := fcList(e)
		if err != nil {
			fail("fc-list: %v", err)
		}
		if !strings.Contains(string(out), f.Path) {
			fail("fontconfig does not list %s", f.Path)
		}
		fmt.Println("ok: fontconfig lists the font")
	}
}

func verifyUserRemoved(e *env, f installed) {
	mustNotExist(f.Path)
	for _, x := range f.Extras {
		mustNotExist(x)
	}
	if runtime.GOOS == "linux" {
		out, _ := fcList(e)
		if strings.Contains(string(out), f.Path) {
			fail("fontconfig still lists %s", f.Path)
		}
		fmt.Println("ok: fontconfig no longer lists the font")
	}
}

func testMachine(e *env, ids []int64, want int) {
	fmt.Println("machine-wide mode is Windows-only; skipped")
}

func fcList(e *env) ([]byte, error) {
	cmd := exec.Command("fc-list")
	cmd.Env = append(os.Environ(), e.extra...)
	return cmd.CombinedOutput()
}
