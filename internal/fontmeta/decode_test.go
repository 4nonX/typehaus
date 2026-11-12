package fontmeta

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeOne(t *testing.T, name string, b []byte) File {
	t.Helper()
	fs, err := Decode(name, b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(fs) != 1 {
		t.Fatalf("%s: want 1 file, got %d", name, len(fs))
	}
	return fs[0]
}

func TestDecodeWebFontsConverted(t *testing.T) {
	for _, n := range []string{"Poppins-Regular.woff", "Poppins-Regular.woff2"} {
		f := decodeOne(t, n, readFixture(t, n))
		if f.Kind != KindSFNT || f.Format != "ttf" || f.Name != "Poppins-Regular.ttf" {
			t.Errorf("%s: %+v", n, f)
		}
		if f.Faces[0].Family != "Poppins" || f.Source == "" {
			t.Errorf("%s: family %q source %q", n, f.Faces[0].Family, f.Source)
		}
		if !isSFNT(f.Data) {
			t.Errorf("%s: converted data is not sfnt", n)
		}
	}
}

// minimal Windows .fnt (version 2.0 header) with a face name
func makeFNT(face string, points, weight int, italic bool) []byte {
	b := make([]byte, 0x76+len(face)+1)
	binary.LittleEndian.PutUint16(b[0:], 0x200)
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b)))
	copy(b[6:], "(c) test")
	binary.LittleEndian.PutUint16(b[0x44:], uint16(points))
	if italic {
		b[0x50] = 1
	}
	binary.LittleEndian.PutUint16(b[0x53:], uint16(weight))
	binary.LittleEndian.PutUint32(b[0x69:], 0x76)
	copy(b[0x76:], face)
	return b
}

// makeNEFon wraps FNT resources into a 16-bit NE .fon executable.
func makeNEFon(fnts ...[]byte) []byte {
	const ne = 0x80
	const rt = 0x40 // resource table offset relative to NE header
	var b bytes.Buffer
	b.Write([]byte("MZ"))
	b.Write(make([]byte, 0x3a))
	binary.Write(&b, binary.LittleEndian, uint32(ne))
	b.Write(make([]byte, ne-b.Len()))
	hdr := make([]byte, rt)
	copy(hdr, "NE")
	binary.LittleEndian.PutUint16(hdr[0x24:], rt)
	b.Write(hdr)
	// resource table: shift 0, one type (RT_FONT) with len(fnts) entries
	tableLen := 2 + 8 + 12*len(fnts) + 2
	dataStart := b.Len() + tableLen
	binary.Write(&b, binary.LittleEndian, uint16(0))
	binary.Write(&b, binary.LittleEndian, uint16(0x8008))
	binary.Write(&b, binary.LittleEndian, uint16(len(fnts)))
	b.Write(make([]byte, 4))
	off := dataStart
	for _, f := range fnts {
		binary.Write(&b, binary.LittleEndian, uint16(off))
		binary.Write(&b, binary.LittleEndian, uint16(len(f)))
		b.Write(make([]byte, 8))
		off += len(f)
	}
	binary.Write(&b, binary.LittleEndian, uint16(0))
	for _, f := range fnts {
		b.Write(f)
	}
	return b.Bytes()
}

func TestDecodeWindowsBitmap(t *testing.T) {
	fon := makeNEFon(makeFNT("Terminal", 9, 400, false), makeFNT("Terminal", 12, 400, false), makeFNT("Terminal", 12, 700, false))
	f := decodeOne(t, "TERMINAL.FON", fon)
	if f.Kind != KindWinFNT || f.Format != "fon" || len(f.Faces) != 2 {
		t.Fatalf("fon: %+v", f)
	}
	if f.Faces[0].Family != "Terminal" || f.Faces[0].Style != "Regular (9, 12 pt)" || f.Faces[1].Style != "Bold (12 pt)" {
		t.Errorf("fon faces: %+v", f.Faces)
	}
	if p := KindWinFNT.Platforms(false); len(p) != 2 {
		t.Errorf("winfnt platforms %v", p)
	}
	fnt := decodeOne(t, "x.fnt", makeFNT("Courier", 10, 400, true))
	if fnt.Format != "fnt" || fnt.Faces[0].Style != "Italic (10 pt)" {
		t.Errorf("fnt: %+v", fnt.Faces)
	}
	// optional: the real Windows file from the issue, if present locally
	if b, err := os.ReadFile("/tmp/8514OEM.FON"); err == nil {
		f := decodeOne(t, "8514OEM.FON", b)
		t.Logf("8514OEM.FON → %+v", f.Faces)
	}
}

// makeResourceFork builds a minimal Mac resource fork with the given
// resources of one type.
func makeResourceFork(typ string, datas ...[]byte) []byte {
	var data bytes.Buffer
	var offs []int
	for _, d := range datas {
		offs = append(offs, data.Len())
		binary.Write(&data, binary.BigEndian, uint32(len(d)))
		data.Write(d)
	}
	var m bytes.Buffer
	m.Write(make([]byte, 24))                      // header copy + handle + file ref + attrs
	binary.Write(&m, binary.BigEndian, uint16(28)) // type list offset (from map start)
	binary.Write(&m, binary.BigEndian, uint16(28+2+8+12*len(datas)))
	binary.Write(&m, binary.BigEndian, uint16(0)) // types - 1
	m.WriteString(typ)
	binary.Write(&m, binary.BigEndian, uint16(len(datas)-1))
	binary.Write(&m, binary.BigEndian, uint16(2+8)) // ref list offset from type list
	for i, o := range offs {
		binary.Write(&m, binary.BigEndian, uint16(128+i))
		binary.Write(&m, binary.BigEndian, uint16(0xffff))
		binary.Write(&m, binary.BigEndian, uint32(o))
		m.Write(make([]byte, 4))
	}
	var out bytes.Buffer
	binary.Write(&out, binary.BigEndian, uint32(256))
	binary.Write(&out, binary.BigEndian, uint32(256+data.Len()))
	binary.Write(&out, binary.BigEndian, uint32(data.Len()))
	binary.Write(&out, binary.BigEndian, uint32(m.Len()))
	out.Write(make([]byte, 256-16))
	out.Write(data.Bytes())
	out.Write(m.Bytes())
	return out.Bytes()
}

func TestDecodeMacFonts(t *testing.T) {
	ttf := readFixture(t, "Poppins-Regular.ttf")
	f := decodeOne(t, "Poppins.dfont", makeResourceFork("sfnt", ttf))
	if f.Kind != KindSFNT || f.Source != "dfont" || f.Faces[0].Family != "Poppins" {
		t.Fatalf("dfont: %+v", f)
	}
	// suitcase delivered as AppleDouble (zip from a Mac: __MACOSX/._Name)
	rf := makeResourceFork("sfnt", ttf)
	var ad bytes.Buffer
	binary.Write(&ad, binary.BigEndian, uint32(0x00051607))
	binary.Write(&ad, binary.BigEndian, uint32(0x00020000))
	ad.Write(make([]byte, 16))
	binary.Write(&ad, binary.BigEndian, uint16(1))
	binary.Write(&ad, binary.BigEndian, uint32(2))  // resource fork
	binary.Write(&ad, binary.BigEndian, uint32(38)) // offset
	binary.Write(&ad, binary.BigEndian, uint32(len(rf)))
	ad.Write(rf)
	f = decodeOne(t, "Poppins", ad.Bytes())
	if f.Source != "suitcase" || f.Faces[0].Family != "Poppins" {
		t.Fatalf("suitcase: %+v", f)
	}
	// AppleDouble without resource fork (plain Finder info) is not a font
	if _, err := Decode("._x", []byte{0, 5, 0x16, 7, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("AppleDouble without resource fork should not decode")
	}
}

func TestDecodeType1(t *testing.T) {
	pfa := []byte("%!PS-AdobeFont-1.0: Demo-BoldItalic 001.000\n/FontInfo 10 dict dup begin\n/version (001.000) readonly def\n" +
		"/Notice (Copyright test) readonly def\n/FullName (Demo Bold Italic) readonly def\n/FamilyName (Demo) readonly def\n" +
		"/Weight (Bold) readonly def\n/ItalicAngle -12 def\nend readonly def\n/FontName /Demo-BoldItalic def\ncurrentfile eexec\n")
	f := decodeOne(t, "demo.pfa", pfa)
	fc := f.Faces[0]
	if f.Kind != KindType1 || fc.Family != "Demo" || fc.Style != "Bold Italic" || fc.Weight != 700 || !fc.Italic || fc.PostScriptName != "Demo-BoldItalic" {
		t.Fatalf("pfa: %+v", fc)
	}
	// PFB = segment-wrapped
	var pfb bytes.Buffer
	pfb.Write([]byte{0x80, 1})
	binary.Write(&pfb, binary.LittleEndian, uint32(len(pfa)))
	pfb.Write(pfa)
	pfb.Write([]byte{0x80, 3})
	if f := decodeOne(t, "demo.pfb", pfb.Bytes()); f.Format != "pfb" || f.Faces[0].Family != "Demo" {
		t.Fatalf("pfb: %+v", f)
	}
	if _, err := Decode("demo.afm", []byte("StartFontMetrics 4.1\n")); err == nil {
		t.Error("lone .afm should be rejected with a hint")
	}
	if p := KindType1.Platforms(true); len(p) != 2 {
		t.Errorf("type1 with pfm platforms %v", p)
	}
	if p := KindType1.Platforms(false); len(p) != 1 || p[0] != "linux" {
		t.Errorf("type1 without pfm platforms %v", p)
	}
	// real Type 1 fonts installed on the test machine, if any
	files, _ := filepath.Glob("/usr/share/fonts/X11/Type1/*.pfb")
	for _, p := range files[:min(3, len(files))] {
		b, _ := os.ReadFile(p)
		f := decodeOne(t, p, b)
		t.Logf("%s → %s %s", filepath.Base(p), f.Faces[0].Family, f.Faces[0].Style)
	}
}

func TestDecodeX11(t *testing.T) {
	bdf := []byte("STARTFONT 2.1\nFONT -misc-fixed-medium-r-normal--13-120-75-75-c-70-iso10646-1\nSIZE 13 75 75\n" +
		"STARTPROPERTIES 4\nFAMILY_NAME \"Fixed\"\nWEIGHT_NAME \"Medium\"\nSLANT \"R\"\nPOINT_SIZE 120\nENDPROPERTIES\nCHARS 0\nENDFONT\n")
	f := decodeOne(t, "6x13.bdf", bdf)
	if f.Kind != KindBDF || f.Faces[0].Family != "Fixed" || f.Faces[0].Style != "Medium (12 pt)" {
		t.Fatalf("bdf: %+v", f.Faces)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(bdf)
	w.Close()
	if f := decodeOne(t, "6x13.bdf.gz", gz.Bytes()); f.Source != "bdf.gz" || f.Format != "bdf" {
		t.Fatalf("bdf.gz: %+v", f)
	}
	files, _ := filepath.Glob("/usr/share/fonts/X11/misc/*.pcf.gz")
	files2, _ := filepath.Glob("/usr/share/fonts/X11/*/*.pcf.gz")
	files = append(files, files2...)
	for _, p := range files[:min(3, len(files))] {
		b, _ := os.ReadFile(p)
		f := decodeOne(t, p, b)
		if f.Kind != KindPCF || f.Faces[0].Family == "" {
			t.Errorf("%s: %+v", p, f)
		}
	}
}

func TestDecodeNotFonts(t *testing.T) {
	for name, b := range map[string][]byte{
		"desktop.ini":               []byte("[.ShellClassInfo]\r\n"),
		"fms_metadata.xml":          []byte("\xef\xbb\xbf<?xml version"),
		"GlobalSerif.CompositeFont": []byte("\xef\xbb\xbf<FontFamily"),
		"readme.txt":                []byte("hello world"),
	} {
		if _, err := Decode(name, b); err != ErrNotFont {
			t.Errorf("%s: want ErrNotFont, got %v", name, err)
		}
	}
	if _, err := Decode("broken.ttf", []byte("garbage!garbage!")); err == nil || err == ErrNotFont {
		t.Errorf("unknown .ttf content should be a font error, got %v", err)
	}
}

// TestWriteFixtures regenerates synthetic test files: TYPEHAUS_WRITE_FIXTURES=1 go test -run WriteFixtures
func TestWriteFixtures(t *testing.T) {
	if os.Getenv("TYPEHAUS_WRITE_FIXTURES") == "" {
		t.Skip("set TYPEHAUS_WRITE_FIXTURES=1 to regenerate")
	}
	dir := filepath.Join("..", "..", "testdata", "fonts")
	fon := makeNEFon(makeFNT("Typehaus Test Bitmap", 9, 400, false), makeFNT("Typehaus Test Bitmap", 12, 400, false))
	if err := os.WriteFile(filepath.Join(dir, "TypehausTestBitmap.fon"), fon, 0o644); err != nil {
		t.Fatal(err)
	}
}
