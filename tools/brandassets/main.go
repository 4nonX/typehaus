// Command brandassets regenerates the static brand files checked into the
// repository from internal/brand. Development tool only.
//
//	go run ./tools/brandassets
package main

import (
	"fmt"
	"image"
	"log"
	"os"

	"github.com/4nonX/typehaus/internal/brand"
)

func main() {
	write("internal/server/web/favicon.svg", []byte(brand.SVG()))
	write("internal/agent/ui/web/icon.svg", []byte(brand.SVG()))
	write("docs/logo.svg", []byte(brand.SVG()))
	// Windows executable icon: every size rendered natively, not downscaled
	var icons []image.Image
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		icons = append(icons, brand.Mark(s, brand.Tile, brand.StateOK, 0))
	}
	write("cmd/typehaus-agent/winres/icon.ico", brand.ICO(icons...))
	// macOS app icon source images (iconutil builds the .icns in CI)
	for _, s := range []int{16, 32, 64, 128, 256, 512, 1024} {
		write(fmt.Sprintf("packaging/macos/icon.iconset/%s.png", iconsetName(s)), brand.PNG(brand.Mark(s, brand.Tile, brand.StateOK, 0)))
	}
	for _, s := range []int{32, 256, 512} { // 1x entries that share a pixel size with a 2x entry
		write(fmt.Sprintf("packaging/macos/icon.iconset/icon_%dx%d.png", s, s), brand.PNG(brand.Mark(s, brand.Tile, brand.StateOK, 0)))
	}
}

// iconsetName maps a pixel size to the names iconutil expects (1x and 2x).
func iconsetName(px int) string {
	switch px {
	case 16:
		return "icon_16x16"
	case 32:
		return "icon_16x16@2x"
	case 64:
		return "icon_32x32@2x"
	case 128:
		return "icon_128x128"
	case 256:
		return "icon_128x128@2x"
	case 512:
		return "icon_256x256@2x"
	case 1024:
		return "icon_512x512@2x"
	}
	return fmt.Sprintf("icon_%dx%d", px, px)
}

func write(p string, b []byte) {
	if err := os.MkdirAll(dir(p), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote", p)
}

func dir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
