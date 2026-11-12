package brand

import (
	"fmt"
	"strings"
)

// SVG returns the tile app icon (ink tile, paper "t", accent stop) as SVG,
// using exactly the geometry of the raster renderer.
func SVG() string {
	const size = 64.0
	inner := size * tileFill
	k := inner / 16
	ox := (size-inner)/2 - k*0.1
	oy := (size-inner)/2 + k*0.4
	x := func(v float64) float64 { return v*k + ox }
	y := func(v float64) float64 { return v*k + oy }
	c := kappa
	ro, ri := gOuterR*k, gInnerR*k
	sl, sr, ft, base, fe := x(gStemL), x(gStemR), y(gFootT), y(gBase), x(gFootEnd)

	var p strings.Builder
	f := func(format string, a ...any) { fmt.Fprintf(&p, format, a...) }
	f("M%.2f %.2fL%.2f %.2fL%.2f %.2f", sl, y(gTopL), sr, y(gTopR), sr, ft-ri)
	f("C%.2f %.2f %.2f %.2f %.2f %.2f", sr, ft-ri+ri*c, sr+ri-ri*c, ft, sr+ri, ft)
	f("L%.2f %.2fL%.2f %.2fL%.2f %.2f", fe, ft, fe, base, sl+ro, base)
	f("C%.2f %.2f %.2f %.2f %.2f %.2fZ", sl+ro-ro*c, base, sl, base-ro+ro*c, sl, base-ro)
	f("M%.2f %.2fH%.2fV%.2fH%.2fZ", x(gBarL), y(gBarT), x(gBarR), y(gBarB), x(gBarL))

	r := size * tileRadius
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`+
		`<rect width="64" height="64" rx="%.2f" fill="#1b1a17"/>`+
		`<path d="%s" fill="#f4efe6"/>`+
		`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="#e5482d"/></svg>`+"\n",
		r, p.String(), x(gStopL), y(gStopT), (gStopR-gStopL)*k, (gBase-gStopT)*k)
}
