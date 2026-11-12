// Package brand renders the typehaus mark – a sturdy lowercase "t" followed
// by a square full stop – as raster icons for every size and status.
//
// The mark is drawn on a 16-unit design grid. At render time straight edges
// are snapped to whole pixels for the requested size, so tray icons stay
// crisp at 16, 20, 24 and 32 px instead of being blurry downscales of a large
// bitmap. The square "full stop" doubles as the status light.
package brand

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// State is the agent status shown by the icon.
type State int

const (
	StateOK           State = iota // synced: solid accent stop
	StateSyncing                   // animating: stop alternates solid / outline
	StateOffline                   // server unreachable: outline stop
	StateError                     // install problems: amber attention diamond
	StateDisconnected              // not enrolled: dimmed mark, no stop
)

// Variant selects colours for the surface the icon sits on.
type Variant int

const (
	OnDark   Variant = iota // light glyph, e.g. Windows dark taskbar
	OnLight                 // dark glyph, e.g. Windows light taskbar
	Template                // black + alpha only; macOS tints it for the menu bar
	Tile                    // self-contained ink tile, readable on any background
)

// Palette – identical to the web UI.
var (
	Ink    = color.NRGBA{0x1b, 0x1a, 0x17, 0xff}
	Paper  = color.NRGBA{0xf4, 0xef, 0xe6, 0xff}
	Accent = color.NRGBA{0xe5, 0x48, 0x2d, 0xff}
	Amber  = color.NRGBA{0xf2, 0xa3, 0x1b, 0xff}
	Muted  = color.NRGBA{0x9a, 0x94, 0x88, 0xff}
	White  = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	Black  = color.NRGBA{0x00, 0x00, 0x00, 0xff}
)

// design grid (16 × 16 units)
const (
	gStemL, gStemR       = 4.0, 7.0
	gTopL, gTopR         = 3.0, 1.0 // slanted cut, rising to the right
	gBarL, gBarR         = 1.0, 10.0
	gBarT, gBarB         = 5.0, 7.0
	gFootT, gBase        = 12.0, 14.0
	gFootEnd             = 10.0
	gOuterR, gInnerR     = 3.5, 1.5
	gStopL, gStopR       = 12.0, 15.0
	gStopT               = 11.0
	kappa                = 0.5522847
	tileRadius, tileFill = 0.225, 0.74 // corner radius and glyph scale, relative to tile size
)

type geom struct {
	k, ox, oy float64
	snap      bool
}

func (g geom) x(v float64) float32 {
	p := v*g.k + g.ox
	if g.snap {
		p = math.Round(p)
	}
	return float32(p)
}

func (g geom) y(v float64) float32 {
	p := v*g.k + g.oy
	if g.snap {
		p = math.Round(p)
	}
	return float32(p)
}

func (g geom) len(v float64) float32 { return float32(v * g.k) }

// glyph adds the "t" outline (stem with foot, crossbar) to z.
func glyph(z *vector.Rasterizer, g geom) {
	sl, sr := g.x(gStemL), g.x(gStemR)
	tl, tr := g.y(gTopL), g.y(gTopR)
	base, footT, footEnd := g.y(gBase), g.y(gFootT), g.x(gFootEnd)
	ro, ri := g.len(gOuterR), g.len(gInnerR)
	if ri < 1 {
		ri = 1
	}
	c := float32(kappa)

	// stem + foot, clockwise
	z.MoveTo(sl, tl)
	z.LineTo(sr, tr)
	z.LineTo(sr, footT-ri)
	z.CubeTo(sr, footT-ri+ri*c, sr+ri-ri*c, footT, sr+ri, footT) // inner corner
	z.LineTo(footEnd, footT)
	z.LineTo(footEnd, base)
	z.LineTo(sl+ro, base)
	z.CubeTo(sl+ro-ro*c, base, sl, base-ro+ro*c, sl, base-ro) // outer corner
	z.ClosePath()

	// crossbar
	rect(z, g.x(gBarL), g.y(gBarT), g.x(gBarR), g.y(gBarB))
}

func rect(z *vector.Rasterizer, x0, y0, x1, y1 float32) {
	z.MoveTo(x0, y0)
	z.LineTo(x1, y0)
	z.LineTo(x1, y1)
	z.LineTo(x0, y1)
	z.ClosePath()
}

// rectHole adds a counter-clockwise rectangle, cutting a hole.
func rectHole(z *vector.Rasterizer, x0, y0, x1, y1 float32) {
	z.MoveTo(x0, y0)
	z.LineTo(x0, y1)
	z.LineTo(x1, y1)
	z.LineTo(x1, y0)
	z.ClosePath()
}

type stopShape int

const (
	stopNone stopShape = iota
	stopSolid
	stopRing
	stopDiamond
)

func stop(z *vector.Rasterizer, g geom, shape stopShape) {
	x0, y0, x1, y1 := g.x(gStopL), g.y(gStopT), g.x(gStopR), g.y(gBase)
	if g.snap {
		// keep the stop perfectly square, anchored to the baseline
		s := float32(math.Round((gStopR - gStopL) * g.k))
		x0, y0 = x1-s, y1-s
	}
	switch shape {
	case stopSolid:
		rect(z, x0, y0, x1, y1)
	case stopRing:
		w := float32(math.Max(1, math.Round(g.k*0.85)))
		if !g.snap {
			w = g.len(0.85)
		}
		rect(z, x0, y0, x1, y1)
		rectHole(z, x0+w, y0+w, x1-w, y1-w)
	case stopDiamond:
		// "Attention" diamond, 5 units across, sitting on the baseline and
		// flush with the right edge. Small sizes are built from pixel rows –
		// anti-aliased diagonals turn into mush at 16 px.
		right, bottom := g.x(16), y1
		if g.snap {
			d := int(math.Round(5 * g.k))
			for r := 0; r < d; r++ {
				w := 2*min(r, d-1-r) + 1
				if d%2 == 0 {
					w++
				}
				cx := float64(right) - float64(d)/2
				top := float64(bottom) - float64(d-r)
				rect(z, float32(cx-float64(w)/2), float32(top), float32(cx+float64(w)/2), float32(top+1))
			}
		} else {
			d := g.len(5)
			cx, cy := right-d/2, bottom-d/2
			z.MoveTo(cx, cy-d/2)
			z.LineTo(cx+d/2, cy)
			z.LineTo(cx, cy+d/2)
			z.LineTo(cx-d/2, cy)
			z.ClosePath()
		}
	}
}

// Mark renders the icon at px × px pixels. frame animates StateSyncing.
func Mark(px int, v Variant, st State, frame int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, px, px))
	g := geom{k: float64(px) / 16, snap: px <= 64}

	glyphCol, stopCol := White, Accent
	glyphAlpha := 1.0
	switch v {
	case OnLight:
		glyphCol = Ink
	case Template:
		glyphCol, stopCol = Black, Black
	case Tile:
		glyphCol = Paper
		inner := float64(px) * tileFill
		g.k = inner / 16
		g.ox = (float64(px) - inner) / 2
		g.oy = g.ox + g.k*0.4 // optical centring: the mark is bottom-heavy
		g.ox -= g.k * 0.1
		g.snap = px <= 64
		fill(dst, roundedRect(px, float32(float64(px)*tileRadius)), Ink, 1)
	}

	shape := stopSolid
	switch st {
	case StateSyncing:
		if frame%2 == 1 {
			shape = stopRing
		}
	case StateOffline:
		shape = stopRing
		if v != Template {
			stopCol = Muted
		}
	case StateError:
		shape = stopDiamond
		if v != Template {
			stopCol = Amber
		}
	case StateDisconnected:
		shape = stopNone
		glyphAlpha = 0.45
	}

	z := vector.NewRasterizer(px, px)
	glyph(z, g)
	fill(dst, z, glyphCol, glyphAlpha)
	if shape != stopNone {
		z = vector.NewRasterizer(px, px)
		stop(z, g, shape)
		fill(dst, z, stopCol, 1)
	}
	return dst
}

func roundedRect(px int, r float32) *vector.Rasterizer {
	z := vector.NewRasterizer(px, px)
	s := float32(px)
	c := r * (1 - float32(kappa))
	z.MoveTo(r, 0)
	z.LineTo(s-r, 0)
	z.CubeTo(s-c, 0, s, c, s, r)
	z.LineTo(s, s-r)
	z.CubeTo(s, s-c, s-c, s, s-r, s)
	z.LineTo(r, s)
	z.CubeTo(c, s, 0, s-c, 0, s-r)
	z.LineTo(0, r)
	z.CubeTo(0, c, c, 0, r, 0)
	z.ClosePath()
	return z
}

func fill(dst *image.NRGBA, z *vector.Rasterizer, c color.NRGBA, alpha float64) {
	mask := image.NewAlpha(dst.Bounds())
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	if alpha < 1 {
		for i, a := range mask.Pix {
			mask.Pix[i] = uint8(float64(a)*alpha + 0.5)
		}
	}
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// Upscale enlarges img by an integer factor with nearest-neighbour sampling.
// Used to hand Windows a large icon that downsamples exactly to the
// pixel-snapped small rendering.
func Upscale(img *image.NRGBA, f int) *image.NRGBA {
	if f <= 1 {
		return img
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*f, b.Dy()*f))
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			out.SetNRGBA(x, y, img.NRGBAAt(x/f, y/f))
		}
	}
	return out
}
