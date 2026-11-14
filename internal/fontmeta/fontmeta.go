// Package fontmeta reads naming and classification metadata from
// OpenType / TrueType fonts and font collections without rasterising them.
package fontmeta

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Format of a font file.
type Format string

const (
	FormatTTF Format = "ttf"
	FormatOTF Format = "otf"
	FormatTTC Format = "ttc"
)

// Face describes one font face (a TTC contains several).
type Face struct {
	Family         string `json:"family"`
	Style          string `json:"style"`
	FullName       string `json:"full_name"`
	PostScriptName string `json:"postscript_name"`
	Version        string `json:"version"`
	Designer       string `json:"designer,omitempty"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	Copyright      string `json:"copyright,omitempty"`
	License        string `json:"license,omitempty"`
	LicenseURL     string `json:"license_url,omitempty"`
	Weight         int    `json:"weight"`
	Italic         bool   `json:"italic"`
	// FSType is the OS/2 embedding permission field (0 = installable).
	FSType uint16 `json:"fs_type"`
	// VendorID is the OS/2 achVendID, the foundry's registered four-letter
	// code; Trademark is name ID 7. Both help recognise commercial foundries.
	VendorID  string `json:"vendor_id,omitempty"`
	Trademark string `json:"trademark,omitempty"`
	// Class is the design classification as the font itself declares it
	// (OS/2 family class, else PANOSE): sans-serif, serif, slab-serif,
	// monospace, handwriting, display, symbol – "" when it doesn't say.
	Class string `json:"class,omitempty"`
	// Variable: the font has variation axes (an fvar table).
	Variable bool `json:"variable,omitempty"`
	// Width is the OS/2 width class: 1 ultra-condensed … 5 normal … 9
	// ultra-expanded.
	Width int `json:"width,omitempty"`
	// Fingerprint identifies the font itself, independent of its file
	// (see fingerprint); empty for formats other than TrueType/OpenType.
	Fingerprint string `json:"-"`
}

// Info is the parsed result for a whole file.
type Info struct {
	Format Format `json:"format"`
	Faces  []Face `json:"faces"`
}

var (
	ErrWebFont     = errors.New("WOFF/WOFF2 web fonts cannot be installed on desktops; upload the TTF/OTF original")
	ErrUnsupported = errors.New("not a TrueType/OpenType font")
)

// Parse inspects b and returns its metadata.
func Parse(b []byte) (*Info, error) {
	if len(b) < 12 {
		return nil, ErrUnsupported
	}
	tag := string(b[:4])
	switch tag {
	case "wOFF", "wOF2":
		return nil, ErrWebFont
	case "ttcf":
		n := int(be32(b, 8))
		if n <= 0 || n > 1024 || len(b) < 12+4*n {
			return nil, fmt.Errorf("corrupt font collection header")
		}
		info := &Info{Format: FormatTTC}
		for i := 0; i < n; i++ {
			off := int(be32(b, 12+4*i))
			f, err := parseFace(b, off)
			if err != nil {
				return nil, fmt.Errorf("collection face %d: %w", i, err)
			}
			info.Faces = append(info.Faces, *f)
		}
		return info, nil
	case "\x00\x01\x00\x00", "true":
		f, err := parseFace(b, 0)
		if err != nil {
			return nil, err
		}
		return &Info{Format: FormatTTF, Faces: []Face{*f}}, nil
	case "OTTO":
		f, err := parseFace(b, 0)
		if err != nil {
			return nil, err
		}
		return &Info{Format: FormatOTF, Faces: []Face{*f}}, nil
	}
	return nil, ErrUnsupported
}

type table struct{ off, length int }

func parseFace(b []byte, base int) (*Face, error) {
	if base < 0 || base+12 > len(b) {
		return nil, fmt.Errorf("offset table out of range")
	}
	num := int(be16(b, base+4))
	if base+12+16*num > len(b) {
		return nil, fmt.Errorf("table directory out of range")
	}
	tables := map[string]table{}
	for i := 0; i < num; i++ {
		rec := base + 12 + 16*i
		t := table{off: int(be32(b, rec+8)), length: int(be32(b, rec+12))}
		if t.off < 0 || t.length < 0 || t.off+t.length > len(b) {
			continue
		}
		tables[string(b[rec:rec+4])] = t
	}
	nt, ok := tables["name"]
	if !ok {
		return nil, fmt.Errorf("font has no name table")
	}
	names := parseNames(b[nt.off : nt.off+nt.length])

	f := &Face{
		Family:         first(names[16], names[1]),
		Style:          first(names[17], names[2]),
		FullName:       names[4],
		PostScriptName: names[6],
		Version:        strings.TrimSpace(strings.TrimPrefix(names[5], "Version")),
		Copyright:      names[0],
		Manufacturer:   names[8],
		Designer:       names[9],
		License:        names[13],
		LicenseURL:     names[14],
		Trademark:      names[7],
		Weight:         400,
	}
	if f.Style == "" {
		f.Style = "Regular"
	}
	if names[16] == "" {
		f.Family, f.Style = legacyFamily(f.Family, f.Style)
	}
	if f.FullName == "" {
		f.FullName = strings.TrimSpace(f.Family + " " + f.Style)
	}
	if f.Family == "" {
		return nil, fmt.Errorf("font has no family name")
	}
	if os2, ok := tables["OS/2"]; ok && os2.length >= 64 {
		t := b[os2.off : os2.off+os2.length]
		f.Weight = int(be16(t, 4))
		f.FSType = be16(t, 8)
		f.VendorID = strings.TrimSpace(strings.Trim(string(t[58:62]), "\x00"))
		fsSel := be16(t, 62)
		f.Italic = fsSel&0x1 != 0 || fsSel&0x200 != 0
		f.Width = int(be16(t, 6))
		f.Class = classify(t)
	} else {
		ls := strings.ToLower(f.Style)
		f.Italic = strings.Contains(ls, "italic") || strings.Contains(ls, "oblique")
	}
	if f.Weight < 1 || f.Weight > 1000 {
		f.Weight = 400
	}
	if f.Width < 1 || f.Width > 9 {
		f.Width = 5
	}
	if post, ok := tables["post"]; ok && post.length >= 16 && be32(b, post.off+12) != 0 {
		f.Class = "monospace" // isFixedPitch
	}
	_, f.Variable = tables["fvar"]
	for _, color := range []string{"CBDT", "sbix", "SVG "} {
		if _, ok := tables[color]; ok && f.Class == "monospace" {
			f.Class = "symbol" // colour emoji fonts claim fixed pitch
		}
	}
	f.Fingerprint = fingerprint(b, tables, names)
	return f, nil
}

// classify reads the design classification from an OS/2 table: the IBM
// font family class (sFamilyClass) where the foundry set one, else PANOSE.
func classify(os2 []byte) string {
	p := os2[32:42] // PANOSE
	if (p[0] == 2 || p[0] == 0) && p[3] == 9 {
		return "monospace" // Latin text (or unset), proportion monospaced
	}
	switch os2[30] { // class; os2[31] is the subclass
	case 1, 2, 3, 7: // oldstyle, transitional, modern, freeform serifs
		return "serif"
	case 4, 5: // clarendon, slab
		return "slab-serif"
	case 8:
		return "sans-serif"
	case 9: // ornamentals
		return "display"
	case 10:
		return "handwriting" // scripts
	case 12:
		return "symbol"
	}
	switch p[0] { // family kind
	case 2: // Latin text
		switch {
		case p[1] == 6: // square serifs
			return "slab-serif"
		case p[1] >= 2 && p[1] <= 10:
			return "serif"
		case p[1] >= 11 && p[1] <= 15:
			return "sans-serif"
		}
	case 3:
		return "handwriting"
	case 4:
		return "display" // decorative
	case 5:
		return "symbol" // pictorial
	}
	return ""
}

// parseNames returns the best string for each name ID, preferring
// Windows/Unicode English, then any Windows/Unicode, then Mac Roman.
func parseNames(t []byte) map[int]string {
	out := map[int]string{}
	if len(t) < 6 {
		return out
	}
	count := int(be16(t, 2))
	strOff := int(be16(t, 4))
	score := map[int]int{}
	for i := 0; i < count; i++ {
		r := 6 + 12*i
		if r+12 > len(t) {
			break
		}
		platform, enc, lang := be16(t, r), be16(t, r+2), be16(t, r+4)
		id := int(be16(t, r+6))
		ln, off := int(be16(t, r+8)), int(be16(t, r+10))
		start := strOff + off
		if start < 0 || start+ln > len(t) {
			continue
		}
		raw := t[start : start+ln]
		var s string
		var sc int
		switch {
		case platform == 3 && (enc == 1 || enc == 10):
			s = decodeUTF16(raw)
			sc = 3
			if lang == 0x409 {
				sc = 5
			}
		case platform == 0:
			s = decodeUTF16(raw)
			sc = 2
		case platform == 1 && enc == 0:
			s = decodeLatin1(raw)
			sc = 1
			if lang == 0 {
				sc = 2
			}
		default:
			continue
		}
		s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
		if s == "" {
			continue
		}
		if sc > score[id] {
			score[id] = sc
			out[id] = s
		}
	}
	return out
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func decodeLatin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func be16(b []byte, o int) uint16 {
	if o+2 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint16(b[o:])
}

func be32(b []byte, o int) uint32 {
	if o+4 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint32(b[o:])
}
