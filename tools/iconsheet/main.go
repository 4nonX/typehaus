// Command iconsheet renders a review sheet of all tray icon variants, states
// and sizes on simulated taskbars / menu bars. Development tool only.
//
//	go run ./tools/iconsheet -o iconsheet.png
package main

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"

	"github.com/4nonX/typehaus/internal/brand"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type row struct {
	label string
	bg    color.NRGBA
	v     brand.Variant
	tint  *color.NRGBA // template tint (macOS draws template icons in this colour)
}

func main() {
	out := flag.String("o", "iconsheet.png", "output file")
	zoom := flag.Int("zoom", 6, "magnification for the detail view")
	flag.Parse()

	white := color.NRGBA{255, 255, 255, 255}
	black := color.NRGBA{0, 0, 0, 230}
	rows := []row{
		{"Windows dark taskbar", color.NRGBA{0x20, 0x20, 0x20, 255}, brand.OnDark, nil},
		{"Windows light taskbar", color.NRGBA{0xee, 0xee, 0xee, 255}, brand.OnLight, nil},
		{"macOS light menu bar", color.NRGBA{0xe6, 0xe4, 0xe0, 255}, brand.Template, &black},
		{"macOS dark menu bar", color.NRGBA{0x2a, 0x29, 0x2b, 255}, brand.Template, &white},
		{"Linux panel (tile)", color.NRGBA{0x31, 0x36, 0x3b, 255}, brand.Tile, nil},
		{"Linux light panel (tile)", color.NRGBA{0xf2, 0xf2, 0xf2, 255}, brand.Tile, nil},
	}
	states := []struct {
		name  string
		st    brand.State
		frame int
	}{
		{"synced", brand.StateOK, 0}, {"sync 1", brand.StateSyncing, 0}, {"sync 2", brand.StateSyncing, 1},
		{"offline", brand.StateOffline, 0}, {"error", brand.StateError, 0}, {"not connected", brand.StateDisconnected, 0},
	}
	sizes := []int{16, 20, 24, 32}

	labelW, cellW := 190, 0
	for _, s := range sizes {
		cellW += s + 10
	}
	detailW := 16**zoom + 16
	colW := cellW + detailW + 20
	rowH := 16**zoom + 40
	W := labelW + colW*len(states)
	H := 40 + rowH*len(rows) + 300
	sheet := image.NewNRGBA(image.Rect(0, 0, W, H))
	draw.Draw(sheet, sheet.Bounds(), image.NewUniform(color.NRGBA{0xf4, 0xef, 0xe6, 255}), image.Point{}, draw.Src)

	for i, s := range states {
		text(sheet, labelW+i*colW+8, 24, s.name)
	}
	for r, rw := range rows {
		y := 40 + r*rowH
		text(sheet, 8, y+rowH/2, rw.label)
		for i, s := range states {
			x := labelW + i*colW
			draw.Draw(sheet, image.Rect(x, y, x+colW-12, y+rowH-12), image.NewUniform(rw.bg), image.Point{}, draw.Src)
			cx := x + 8
			for _, sz := range sizes {
				ic := tint(brand.Mark(sz, rw.v, s.st, s.frame), rw.tint)
				draw.Draw(sheet, image.Rect(cx, y+12, cx+sz, y+12+sz), ic, image.Point{}, draw.Over)
				cx += sz + 10
			}
			// magnified 16 px rendering
			ic := tint(brand.Mark(16, rw.v, s.st, s.frame), rw.tint)
			big := brand.Upscale(ic, *zoom)
			draw.Draw(sheet, image.Rect(cx+8, y+8, cx+8+16**zoom, y+8+16**zoom), big, image.Point{}, draw.Over)
		}
	}
	// 125 % / 150 % / 200 % tray sizes, magnified
	yy := 40 + rowH*len(rows) + 300 - 40
	_ = yy
	// app icon sizes
	y := 40 + rowH*len(rows) + 20
	text(sheet, 8, y+20, "App icon (tile)")
	x := labelW
	for _, sz := range []int{256, 128, 64, 48, 32, 16} {
		draw.Draw(sheet, image.Rect(x, y, x+sz, y+sz), brand.Mark(sz, brand.Tile, brand.StateOK, 0), image.Point{}, draw.Over)
		x += sz + 24
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, sheet)
}

func tint(img *image.NRGBA, c *color.NRGBA) *image.NRGBA {
	if c == nil {
		return img
	}
	out := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2] = c.R, c.G, c.B
		out.Pix[i+3] = uint8(uint16(img.Pix[i+3]) * uint16(c.A) / 255)
	}
	return out
}

func text(dst draw.Image, x, y int, s string) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(color.NRGBA{0x1b, 0x1a, 0x17, 255}), Face: basicfont.Face7x13,
		Dot: fixed.P(x, y)}
	d.DrawString(s)
}
