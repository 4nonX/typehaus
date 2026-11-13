package fontmeta

import "strings"

// Words that name a weight, width or slope at the end of a legacy family name.
// Longest forms first so "Extra Bold" is not read as "Bold".
var styleWords = []string{
	"extra light", "ultra light", "extra bold", "ultra bold", "extra black", "ultra black",
	"semi bold", "demi bold", "semi light", "extra condensed", "semi condensed", "ultra condensed",
	"extra expanded", "semi expanded",
	"hairline", "thin", "extralight", "ultralight", "light", "semilight", "book", "regular", "normal",
	"medium", "semibold", "demibold", "demi", "bold", "extrabold", "ultrabold", "heavy", "black",
	"extrablack", "ultrablack", "fat",
	"condensed", "cond", "narrow", "compressed", "extended", "expanded", "wide",
	"semicondensed", "extracondensed", "ultracondensed", "semiexpanded", "extraexpanded",
	"italic", "oblique",
}

// ribbi are the four style names legacy (name ID 1/2) naming allows.
var ribbi = map[string]bool{"regular": true, "italic": true, "bold": true, "bold italic": true}

// legacyFamily derives the family and style of a font that has no typographic
// family (name ID 16). Older and simpler tools name every weight beyond the
// four RIBBI styles as its own family: "Helvetica Light" with style "Regular".
// The trailing weight/width words then belong to the style, the way font
// managers group such fonts: family "Helvetica", style "Light". Names that do
// not end in such a word ("Helvetica Rounded") are left as they are.
func legacyFamily(family, style string) (string, string) {
	if !ribbi[strings.ToLower(strings.TrimSpace(style))] {
		return family, style
	}
	base := strings.TrimSpace(family)
	var moved []string
	for {
		low := strings.ToLower(base)
		hit := ""
		for _, w := range styleWords {
			if strings.HasSuffix(low, " "+w) {
				hit = base[len(base)-len(w):]
				break
			}
		}
		if hit == "" {
			break
		}
		rest := strings.TrimSpace(base[:len(base)-len(hit)])
		if rest == "" {
			break
		}
		moved = append([]string{hit}, moved...)
		base = rest
	}
	if len(moved) == 0 {
		return family, style
	}
	st := strings.Join(moved, " ")
	if !strings.EqualFold(style, "regular") {
		st += " " + style
	}
	return base, st
}
