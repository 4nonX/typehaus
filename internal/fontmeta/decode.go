package fontmeta

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/tdewolff/font"
)

// Kind groups formats by how operating systems install them.
type Kind string

const (
	KindSFNT   Kind = "sfnt"   // TrueType / OpenType incl. collections, variable, colour, bitmap-in-sfnt
	KindType1  Kind = "type1"  // PostScript Type 1 (.pfb/.pfa + .pfm/.afm metrics)
	KindWinFNT Kind = "winfnt" // Windows bitmap/vector fonts (.fon, .fnt)
	KindPCF    Kind = "pcf"    // X11 Portable Compiled Format
	KindBDF    Kind = "bdf"    // X11 Bitmap Distribution Format
)

// Platforms that can install a kind of font. Nil means every platform.
func (k Kind) Platforms(hasPFM bool) []string {
	switch k {
	case KindType1:
		if hasPFM {
			return []string{"windows", "linux"}
		}
		return []string{"linux"}
	case KindWinFNT:
		return []string{"windows", "linux"}
	case KindPCF, KindBDF:
		return []string{"linux"}
	}
	return nil
}

// File is one installable font file produced from an upload. Web and Mac
// container formats are converted to plain TrueType/OpenType here, so they
// install everywhere.
type File struct {
	Name   string // file name to store it under (extension matches Data)
	Data   []byte
	Kind   Kind
	Format string // ttf, otf, ttc, pfb, pfa, fon, fnt, pcf, bdf
	Faces  []Face
	// Source is the original format when the file was converted
	// (woff, woff2, eot, dfont, suitcase, lwfn, pcf.gz).
	Source string
}

// ErrNotFont is returned for files that are not fonts at all (or only
// metrics/companion files without the font itself).
var ErrNotFont = errors.New("not a font file")

// CompanionExts are metric/companion files that accompany PostScript Type 1
// fonts. They are attached to the matching .pfb/.pfa, not imported alone.
var CompanionExts = map[string]bool{".pfm": true, ".afm": true, ".inf": true}

// Decode identifies any supported font format and returns the installable
// file(s) it contains. A Mac suitcase can contain several fonts.
func Decode(name string, b []byte) ([]File, error) {
	ext := strings.ToLower(filepath.Ext(name))
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if len(b) < 4 {
		return nil, ErrNotFont
	}
	if CompanionExts[ext] {
		return nil, fmt.Errorf("%s is a metrics file for a PostScript Type 1 font – upload it together with the matching .pfb or .pfa", ext)
	}
	switch {
	case isSFNT(b):
		return sfntFile(base, b, "")
	case bytes.HasPrefix(b, []byte("wOFF")), bytes.HasPrefix(b, []byte("wOF2")), isEOT(b):
		src := map[string]string{"wOFF": "woff", "wOF2": "woff2"}[string(b[:4])]
		if src == "" {
			src = "eot"
		}
		sfnt, err := font.ToSFNT(b)
		if err != nil {
			return nil, fmt.Errorf("cannot convert %s: %w", strings.ToUpper(src), err)
		}
		return sfntFile(base, sfnt, src)
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}): // gzip, e.g. .pcf.gz / .bdf.gz
		raw, err := io.ReadAll(io.LimitReader(mustGzip(b), 64<<20))
		if err != nil || len(raw) < 4 {
			return nil, fmt.Errorf("cannot decompress: %v", err)
		}
		files, err := Decode(strings.TrimSuffix(name, filepath.Ext(name)), raw)
		for i := range files {
			if files[i].Source == "" {
				files[i].Source = files[i].Format + ".gz"
			}
		}
		return files, err
	case b[0] == 0x80 && b[1] == 0x01: // PFB
		return type1File(base, b, "pfb", "")
	case bytes.HasPrefix(b, []byte("%!PS-AdobeFont")), bytes.HasPrefix(b, []byte("%!FontType1")):
		return type1File(base, b, "pfa", "")
	case bytes.HasPrefix(b, []byte("MZ")):
		return winFON(base, b)
	case isFNT(b):
		return winFNT(base, b)
	case bytes.HasPrefix(b, []byte("\x01fcp")):
		return pcfFile(base, b)
	case bytes.HasPrefix(b, []byte("STARTFONT")):
		return bdfFile(base, b)
	case binary.BigEndian.Uint32(b) == 0x00051607: // AppleDouble (resource fork of a Mac file from a zip)
		rf, err := appleDoubleResourceFork(b)
		if err != nil {
			return nil, err
		}
		return resourceForkFonts(base, rf, "suitcase")
	case looksLikeResourceFork(b): // .dfont and data-fork suitcases
		return resourceForkFonts(base, b, "dfont")
	case bytes.HasPrefix(b, []byte("<?xml")), bytes.HasPrefix(b, []byte("\xef\xbb\xbf<")):
		if ext == ".svg" {
			return nil, errors.New("SVG fonts are not supported by any desktop operating system – convert them to OpenType")
		}
		return nil, ErrNotFont
	}
	if knownFontExt[ext] {
		return nil, fmt.Errorf("unrecognised %s file (starts with % x)", ext, b[:min(8, len(b))])
	}
	return nil, ErrNotFont
}

var knownFontExt = map[string]bool{
	".ttf": true, ".otf": true, ".ttc": true, ".otc": true, ".woff": true, ".woff2": true, ".eot": true,
	".dfont": true, ".pfb": true, ".pfa": true, ".fon": true, ".fnt": true, ".pcf": true, ".bdf": true,
	".otb": true, ".suit": true,
}

func mustGzip(b []byte) io.Reader {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return bytes.NewReader(nil)
	}
	return r
}

func isSFNT(b []byte) bool {
	switch string(b[:4]) {
	case "\x00\x01\x00\x00", "true", "OTTO", "ttcf":
		return true
	}
	return false
}

func sfntFile(base string, b []byte, source string) ([]File, error) {
	info, err := Parse(b)
	if err != nil {
		return nil, err
	}
	return []File{{Name: base + "." + string(info.Format), Data: b, Kind: KindSFNT, Format: string(info.Format), Faces: info.Faces, Source: source}}, nil
}

// ---- EOT ----------------------------------------------------------------------

func isEOT(b []byte) bool {
	// EOTSize (LE) == len, magic 0x504C at offset 34
	return len(b) > 36 && int(binary.LittleEndian.Uint32(b)) == len(b) && binary.LittleEndian.Uint16(b[34:]) == 0x504C
}

// ---- PostScript Type 1 -----------------------------------------------------------

// pfbText returns the cleartext (ASCII) part of a PFB file.
func pfbText(b []byte) string {
	var out strings.Builder
	for len(b) >= 6 && b[0] == 0x80 {
		typ := b[1]
		if typ == 3 {
			break
		}
		n := int(binary.LittleEndian.Uint32(b[2:]))
		if 6+n > len(b) {
			break
		}
		if typ == 1 {
			out.Write(b[6 : 6+n])
		}
		b = b[6+n:]
	}
	return out.String()
}

func psValue(text, key string) string {
	i := strings.Index(text, "/"+key)
	if i < 0 {
		return ""
	}
	s := strings.TrimLeft(text[i+len(key)+1:], " \t")
	switch {
	case strings.HasPrefix(s, "("):
		depth, end := 0, -1
		for j, c := range s {
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					end = j
					break
				}
			}
		}
		if end > 0 {
			return strings.TrimSpace(s[1:end])
		}
	case strings.HasPrefix(s, "/"):
		f := strings.Fields(s[1:])
		if len(f) > 0 {
			return f[0]
		}
	default:
		f := strings.Fields(s)
		if len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

func type1File(base string, b []byte, format, source string) ([]File, error) {
	text := string(b)
	if format == "pfb" {
		text = pfbText(b)
	}
	if !strings.Contains(text, "/FontName") {
		return nil, errors.New("PostScript font without /FontName")
	}
	ps := psValue(text, "FontName")
	f := Face{
		Family:         first(psValue(text, "FamilyName"), ps),
		FullName:       first(psValue(text, "FullName"), ps),
		PostScriptName: ps,
		Version:        psValue(text, "version"),
		Copyright:      first(psValue(text, "Copyright"), psValue(text, "Notice")),
		Weight:         weightFromName(psValue(text, "Weight")),
	}
	f.Style = styleFrom(psValue(text, "Weight"), psValue(text, "ItalicAngle") != "" && psValue(text, "ItalicAngle") != "0")
	if strings.HasPrefix(f.FullName, f.Family+" ") {
		f.Style = strings.TrimPrefix(f.FullName, f.Family+" ")
	}
	f.Italic = strings.Contains(strings.ToLower(f.Style), "italic") || strings.Contains(strings.ToLower(f.Style), "oblique")
	return []File{{Name: base + "." + format, Data: b, Kind: KindType1, Format: format, Faces: []Face{f}, Source: source}}, nil
}

// PFMFace reads the Windows family name from a .pfm metrics file. Windows
// lists a Type 1 font under this name, so it is the best family name.
func PFMFace(b []byte) (family string, weight int, italic bool, ok bool) {
	if len(b) < 0x75 {
		return "", 0, false, false
	}
	face := ""
	if off := int(binary.LittleEndian.Uint32(b[0x69:])); off > 0 {
		face = cString(b, off)
	}
	return face, int(binary.LittleEndian.Uint16(b[0x53:])), b[0x50] != 0, face != ""
}

func weightFromName(w string) int {
	w = strings.ToLower(strings.ReplaceAll(w, " ", ""))
	switch {
	case strings.Contains(w, "thin"), strings.Contains(w, "hairline"):
		return 100
	case strings.Contains(w, "extralight"), strings.Contains(w, "ultralight"):
		return 200
	case strings.Contains(w, "light"):
		return 300
	case strings.Contains(w, "medium"):
		return 500
	case strings.Contains(w, "semibold"), strings.Contains(w, "demibold"), strings.Contains(w, "demi"):
		return 600
	case strings.Contains(w, "extrabold"), strings.Contains(w, "ultrabold"), strings.Contains(w, "heavy"):
		return 800
	case strings.Contains(w, "black"):
		return 900
	case strings.Contains(w, "bold"):
		return 700
	}
	return 400
}

func styleFrom(weight string, italic bool) string {
	s := strings.TrimSpace(weight)
	if s == "" || strings.EqualFold(s, "regular") || strings.EqualFold(s, "normal") || strings.EqualFold(s, "roman") || strings.EqualFold(s, "book") {
		s = "Regular"
	}
	if italic {
		if s == "Regular" {
			return "Italic"
		}
		return s + " Italic"
	}
	return s
}

// ---- Windows .fnt / .fon -----------------------------------------------------------

func isFNT(b []byte) bool {
	if len(b) < 0x76 {
		return false
	}
	v := binary.LittleEndian.Uint16(b)
	size := binary.LittleEndian.Uint32(b[2:])
	return (v == 0x200 || v == 0x300 || v == 0x100) && int(size) <= len(b) && size > 0x76
}

type fntInfo struct {
	face        string
	points      int
	weight      int
	italic      bool
	copyright   string
	vector      bool
	pixelHeight int
}

func parseFNT(b []byte) (fntInfo, bool) {
	if !isFNT(b) {
		return fntInfo{}, false
	}
	fi := fntInfo{
		copyright:   strings.TrimSpace(cString(b[6:66], 0)),
		vector:      binary.LittleEndian.Uint16(b[0x42:])&1 != 0,
		points:      int(binary.LittleEndian.Uint16(b[0x44:])),
		italic:      b[0x50] != 0,
		weight:      int(binary.LittleEndian.Uint16(b[0x53:])),
		pixelHeight: int(binary.LittleEndian.Uint16(b[0x58:])),
	}
	if off := int(binary.LittleEndian.Uint32(b[0x69:])); off > 0 {
		fi.face = cString(b, off)
	}
	if fi.weight < 1 || fi.weight > 1000 {
		fi.weight = 400
	}
	return fi, true
}

func fntFaces(fnts []fntInfo) []Face {
	type key struct {
		fam    string
		weight int
		italic bool
	}
	order := []key{}
	sizes := map[key][]int{}
	var copyright string
	vector := false
	for _, f := range fnts {
		k := key{f.face, f.weight, f.italic}
		if _, ok := sizes[k]; !ok {
			order = append(order, k)
		}
		if f.points > 0 {
			sizes[k] = append(sizes[k], f.points)
		}
		if copyright == "" {
			copyright = f.copyright
		}
		vector = vector || f.vector
	}
	var faces []Face
	for _, k := range order {
		style := styleFrom(map[bool]string{true: "Bold", false: "Regular"}[k.weight >= 600], k.italic)
		if ss := uniqueInts(sizes[k]); len(ss) > 0 && !vector {
			style += " (" + joinInts(ss) + " pt)"
		}
		faces = append(faces, Face{
			Family: k.fam, Style: style, FullName: k.fam + " " + style, PostScriptName: "",
			Copyright: copyright, Weight: k.weight, Italic: k.italic,
		})
	}
	return faces
}

func winFNT(base string, b []byte) ([]File, error) {
	fi, ok := parseFNT(b)
	if !ok || fi.face == "" {
		return nil, errors.New("invalid Windows .fnt font")
	}
	return []File{{Name: base + ".fnt", Data: b, Kind: KindWinFNT, Format: "fnt", Faces: fntFaces([]fntInfo{fi})}}, nil
}

// winFON reads the FONT resources of a 16-bit (NE) or 32-bit (PE) .fon file.
func winFON(base string, b []byte) ([]File, error) {
	if len(b) < 0x40 {
		return nil, ErrNotFont
	}
	off := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if off <= 0 || off+4 > len(b) {
		return nil, errors.New("executable without font resources (not a .fon font)")
	}
	var fnts [][]byte
	switch string(b[off : off+2]) {
	case "NE":
		fnts = neFonts(b, off)
	case "PE":
		fnts = peFonts(b, off)
	}
	var infos []fntInfo
	for _, f := range fnts {
		if fi, ok := parseFNT(f); ok && fi.face != "" {
			infos = append(infos, fi)
		}
	}
	if len(infos) == 0 {
		return nil, errors.New("executable without font resources (not a .fon font)")
	}
	return []File{{Name: base + ".fon", Data: b, Kind: KindWinFNT, Format: "fon", Faces: fntFaces(infos)}}, nil
}

func neFonts(b []byte, ne int) [][]byte {
	if ne+0x26 > len(b) {
		return nil
	}
	rt := ne + int(binary.LittleEndian.Uint16(b[ne+0x24:]))
	if rt+2 > len(b) {
		return nil
	}
	shift := uint(binary.LittleEndian.Uint16(b[rt:]))
	if shift > 16 {
		return nil
	}
	p := rt + 2
	var out [][]byte
	for p+8 <= len(b) {
		typ := binary.LittleEndian.Uint16(b[p:])
		if typ == 0 {
			break
		}
		count := int(binary.LittleEndian.Uint16(b[p+2:]))
		p += 8
		for i := 0; i < count && p+12 <= len(b); i++ {
			start := int(binary.LittleEndian.Uint16(b[p:])) << shift
			length := int(binary.LittleEndian.Uint16(b[p+2:])) << shift
			if typ == 0x8008 && start+length <= len(b) && length > 0 {
				out = append(out, b[start:start+length])
			}
			p += 12
		}
	}
	return out
}

func peFonts(b []byte, pe int) [][]byte {
	if pe+24 > len(b) {
		return nil
	}
	nsec := int(binary.LittleEndian.Uint16(b[pe+6:]))
	optSize := int(binary.LittleEndian.Uint16(b[pe+20:]))
	opt := pe + 24
	if opt+optSize > len(b) || optSize < 2 {
		return nil
	}
	var rsrcRVA uint32
	switch binary.LittleEndian.Uint16(b[opt:]) {
	case 0x10b: // PE32
		if optSize >= 96+8*3 {
			rsrcRVA = binary.LittleEndian.Uint32(b[opt+96+8*2:])
		}
	case 0x20b: // PE32+
		if optSize >= 112+8*3 {
			rsrcRVA = binary.LittleEndian.Uint32(b[opt+112+8*2:])
		}
	}
	if rsrcRVA == 0 {
		return nil
	}
	secs := opt + optSize
	rva2off := func(rva uint32) int {
		for i := 0; i < nsec; i++ {
			s := secs + 40*i
			if s+40 > len(b) {
				return -1
			}
			va := binary.LittleEndian.Uint32(b[s+12:])
			vsize := binary.LittleEndian.Uint32(b[s+8:])
			raw := binary.LittleEndian.Uint32(b[s+20:])
			if vsize == 0 {
				vsize = binary.LittleEndian.Uint32(b[s+16:])
			}
			if rva >= va && rva < va+vsize {
				return int(raw + rva - va)
			}
		}
		return -1
	}
	root := rva2off(rsrcRVA)
	if root < 0 {
		return nil
	}
	var out [][]byte
	var walk func(dir, level int, typeOK bool)
	walk = func(dir, level int, typeOK bool) {
		if dir < 0 || dir+16 > len(b) || level > 3 {
			return
		}
		n := int(binary.LittleEndian.Uint16(b[dir+12:])) + int(binary.LittleEndian.Uint16(b[dir+14:]))
		for i := 0; i < n; i++ {
			e := dir + 16 + 8*i
			if e+8 > len(b) {
				return
			}
			id := binary.LittleEndian.Uint32(b[e:])
			target := binary.LittleEndian.Uint32(b[e+4:])
			if level == 0 && id != 8 { // RT_FONT
				continue
			}
			if target&0x80000000 != 0 {
				walk(root+int(target&0x7fffffff), level+1, true)
				continue
			}
			de := root + int(target)
			if !typeOK || de+8 > len(b) {
				continue
			}
			start := rva2off(binary.LittleEndian.Uint32(b[de:]))
			size := int(binary.LittleEndian.Uint32(b[de+4:]))
			if start >= 0 && start+size <= len(b) {
				out = append(out, b[start:start+size])
			}
		}
	}
	walk(root, 0, false)
	return out
}

// ---- X11 PCF / BDF -------------------------------------------------------------------

func pcfFile(base string, b []byte) ([]File, error) {
	props := pcfProperties(b)
	fam := props["FAMILY_NAME"]
	if fam == "" {
		fam = base
	}
	if f := props["FOUNDRY"]; f != "" && !strings.EqualFold(f, "misc") && !strings.Contains(strings.ToLower(fam), strings.ToLower(f)) {
		fam = fam + " (" + f + ")"
	}
	return []File{{Name: base + ".pcf", Data: b, Kind: KindPCF, Format: "pcf", Faces: []Face{xFace(fam, props)}}}, nil
}

func bdfFile(base string, b []byte) ([]File, error) {
	props := map[string]string{}
	for _, line := range strings.Split(string(b[:min(len(b), 64<<10)]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ENDPROPERTIES") || strings.HasPrefix(line, "CHARS ") {
			break
		}
		k, v, ok := strings.Cut(line, " ")
		if ok {
			props[k] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	fam := first(props["FAMILY_NAME"], base)
	return []File{{Name: base + ".bdf", Data: b, Kind: KindBDF, Format: "bdf", Faces: []Face{xFace(fam, props)}}}, nil
}

func xFace(fam string, props map[string]string) Face {
	weight := props["WEIGHT_NAME"]
	slant := strings.ToUpper(props["SLANT"])
	italic := slant == "I" || slant == "O"
	style := styleFrom(weight, italic)
	if pt := props["POINT_SIZE"]; pt != "" {
		var n int
		fmt.Sscan(pt, &n)
		if n > 0 {
			style += fmt.Sprintf(" (%d pt)", n/10)
		}
	} else if px := props["PIXEL_SIZE"]; px != "" {
		style += " (" + px + " px)"
	}
	return Face{Family: fam, Style: style, FullName: fam + " " + style, Copyright: props["COPYRIGHT"],
		Weight: weightFromName(weight), Italic: italic}
}

func pcfProperties(b []byte) map[string]string {
	out := map[string]string{}
	if len(b) < 8 {
		return out
	}
	n := int(binary.LittleEndian.Uint32(b[4:]))
	for i := 0; i < n && 8+16*(i+1) <= len(b); i++ {
		e := 8 + 16*i
		typ := binary.LittleEndian.Uint32(b[e:])
		off := int(binary.LittleEndian.Uint32(b[e+12:]))
		if typ != 1 || off+8 > len(b) { // PCF_PROPERTIES
			continue
		}
		format := binary.LittleEndian.Uint32(b[off:])
		var bo binary.ByteOrder = binary.LittleEndian
		if format&4 != 0 {
			bo = binary.BigEndian
		}
		nprops := int(bo.Uint32(b[off+4:]))
		p := off + 8
		type prop struct {
			name, val uint32
			isStr     bool
		}
		var ps []prop
		for j := 0; j < nprops && p+9 <= len(b); j++ {
			ps = append(ps, prop{bo.Uint32(b[p:]), bo.Uint32(b[p+5:]), b[p+4] != 0})
			p += 9
		}
		if nprops&3 != 0 {
			p += 4 - nprops&3
		}
		if p+4 > len(b) {
			return out
		}
		strSize := int(bo.Uint32(b[p:]))
		strs := p + 4
		if strs+strSize > len(b) {
			return out
		}
		pool := b[strs : strs+strSize]
		for _, pr := range ps {
			name := cString(pool, int(pr.name))
			if pr.isStr {
				out[name] = cString(pool, int(pr.val))
			} else {
				out[name] = fmt.Sprint(int32(pr.val))
			}
		}
	}
	return out
}

// ---- Mac resource forks (.dfont, suitcases, LWFN) ---------------------------------

func looksLikeResourceFork(b []byte) bool {
	if len(b) < 16 {
		return false
	}
	dataOff := binary.BigEndian.Uint32(b)
	mapOff := binary.BigEndian.Uint32(b[4:])
	dataLen := binary.BigEndian.Uint32(b[8:])
	mapLen := binary.BigEndian.Uint32(b[12:])
	return dataOff == 256 && uint64(mapOff)+uint64(mapLen) <= uint64(len(b)) && uint64(dataOff)+uint64(dataLen) <= uint64(len(b)) && mapLen >= 30
}

func appleDoubleResourceFork(b []byte) ([]byte, error) {
	if len(b) < 26 {
		return nil, ErrNotFont
	}
	n := int(binary.BigEndian.Uint16(b[24:]))
	for i := 0; i < n && 26+12*(i+1) <= len(b); i++ {
		e := 26 + 12*i
		if binary.BigEndian.Uint32(b[e:]) == 2 {
			off := int(binary.BigEndian.Uint32(b[e+4:]))
			l := int(binary.BigEndian.Uint32(b[e+8:]))
			if off+l <= len(b) {
				return b[off : off+l], nil
			}
		}
	}
	return nil, ErrNotFont
}

type resource struct {
	id   int16
	name string
	data []byte
}

func readResources(b []byte, typ string) []resource {
	if !looksLikeResourceFork(b) {
		return nil
	}
	dataOff := int(binary.BigEndian.Uint32(b))
	mapOff := int(binary.BigEndian.Uint32(b[4:]))
	if mapOff+28 > len(b) {
		return nil
	}
	typeList := mapOff + int(binary.BigEndian.Uint16(b[mapOff+24:]))
	nameList := mapOff + int(binary.BigEndian.Uint16(b[mapOff+26:]))
	if typeList+2 > len(b) {
		return nil
	}
	nTypes := int(binary.BigEndian.Uint16(b[typeList:])) + 1
	var out []resource
	for i := 0; i < nTypes && typeList+2+8*(i+1) <= len(b); i++ {
		t := typeList + 2 + 8*i
		if string(b[t:t+4]) != typ {
			continue
		}
		count := int(binary.BigEndian.Uint16(b[t+4:])) + 1
		refs := typeList + int(binary.BigEndian.Uint16(b[t+6:]))
		for j := 0; j < count && refs+12*(j+1) <= len(b); j++ {
			r := refs + 12*j
			id := int16(binary.BigEndian.Uint16(b[r:]))
			nameOff := int(binary.BigEndian.Uint16(b[r+2:]))
			dOff := dataOff + int(binary.BigEndian.Uint32(b[r+4:])&0x00ffffff)
			if dOff+4 > len(b) {
				continue
			}
			l := int(binary.BigEndian.Uint32(b[dOff:]))
			if dOff+4+l > len(b) {
				continue
			}
			name := ""
			if nameOff != 0xffff && nameList+nameOff < len(b) {
				nl := int(b[nameList+nameOff])
				if nameList+nameOff+1+nl <= len(b) {
					name = string(b[nameList+nameOff+1 : nameList+nameOff+1+nl])
				}
			}
			out = append(out, resource{id: id, name: name, data: b[dOff+4 : dOff+4+l]})
		}
	}
	return out
}

func resourceForkFonts(base string, rf []byte, source string) ([]File, error) {
	var files []File
	for i, r := range readResources(rf, "sfnt") {
		fs, err := sfntFile(base, r.data, source)
		if err != nil {
			continue
		}
		name := first(r.name, fs[0].Faces[0].FullName, fmt.Sprintf("%s-%d", base, i))
		fs[0].Name = sanitizeName(name) + "." + fs[0].Format
		files = append(files, fs[0])
	}
	// PostScript outlines (LWFN-style 'POST' resources) → PFB
	if posts := readResources(rf, "POST"); len(posts) > 0 {
		sortResources(posts)
		var pfb bytes.Buffer
		for _, p := range posts {
			if len(p.data) < 2 {
				continue
			}
			typ := p.data[0]
			switch typ {
			case 1, 2:
				pfb.Write([]byte{0x80, typ})
				binary.Write(&pfb, binary.LittleEndian, uint32(len(p.data)-2))
				pfb.Write(p.data[2:])
			case 5:
				pfb.Write([]byte{0x80, 3})
			}
		}
		if pfb.Len() > 0 {
			if fs, err := type1File(base, pfb.Bytes(), "pfb", "lwfn"); err == nil {
				files = append(files, fs...)
			}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("Mac font file without outline fonts (bitmap-only suitcase, or the PostScript outlines are in a separate LWFN file – upload both together)")
	}
	return files, nil
}

func sortResources(r []resource) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].id < r[j-1].id; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

// ---- helpers ----------------------------------------------------------------------

func cString(b []byte, off int) string {
	if off < 0 || off >= len(b) {
		return ""
	}
	end := off
	for end < len(b) && b[end] != 0 && end-off < 256 {
		end++
	}
	return strings.TrimSpace(decodeLatin1(b[off:end]))
}

func sanitizeName(s string) string {
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ' ':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	return strings.TrimSpace(out.String())
}

func uniqueInts(v []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, x := range v {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}
