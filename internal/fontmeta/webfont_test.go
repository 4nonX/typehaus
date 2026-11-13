package fontmeta

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	tfont "github.com/tdewolff/font"
)

// sameTables compares two fonts table by table (DSIG aside).
func sameTables(t *testing.T, what string, want, got []byte) {
	t.Helper()
	_, a, err := sfntTables(want, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := sfntTables(got, 0)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if len(a) != len(b) {
		t.Fatalf("%s: %d tables, want %d", what, len(b), len(a))
	}
	for i := range a {
		if a[i].tag == "head" && b[i].tag == "head" && len(a[i].data) == len(b[i].data) && len(a[i].data) >= 18 {
			// may change on re-encoding: checkSumAdjustment and flags bit 11
			x, y := append([]byte{}, a[i].data...), append([]byte{}, b[i].data...)
			for _, h := range [][]byte{x, y} {
				copy(h[8:12], []byte{0, 0, 0, 0})
				h[16] &^= 0x08
			}
			if !bytes.Equal(x, y) {
				t.Fatalf("%s: table head differs", what)
			}
			continue
		}
		if a[i].tag != b[i].tag || !bytes.Equal(a[i].data, b[i].data) {
			t.Fatalf("%s: table %q differs", what, a[i].tag)
		}
	}
}

func testFonts(t *testing.T) map[string][]byte {
	ttf, err := Decode("Poppins-Regular.woff2", readFixture(t, "Poppins-Regular.woff2"))
	if err != nil {
		t.Fatal(err)
	}
	fonts := map[string][]byte{"Poppins (TrueType)": ttf[0].Data}
	for _, pat := range []string{"/usr/share/fonts/opentype/*/*.otf", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"} {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			if b, err := os.ReadFile(m[0]); err == nil {
				fonts[filepath.Base(m[0])] = b
			}
		}
	}
	return fonts
}

func TestEncodeWOFF2(t *testing.T) {
	for name, src := range testFonts(t) {
		w2, err := EncodeWOFF2(src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(w2)%4 != 0 || binary.BigEndian.Uint32(w2[8:]) != uint32(len(w2)) {
			t.Fatalf("%s: length field", name)
		}
		// typehaus' own decoder and an independent one give back the font
		files, err := Decode("x.woff2", w2)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		sameTables(t, name+" (fontmeta)", src, files[0].Data)
		other, err := tfont.ParseWOFF2(w2)
		if err != nil {
			t.Fatalf("%s: tdewolff: %v", name, err)
		}
		sameTables(t, name+" (tdewolff)", src, other)
		if len(w2) >= len(src) {
			t.Errorf("%s: WOFF2 (%d bytes) not smaller than the font (%d)", name, len(w2), len(src))
		}
		t.Logf("%s: %d → %d bytes", name, len(src), len(w2))
	}
}

func TestEncodeWOFF(t *testing.T) {
	for name, src := range testFonts(t) {
		w, err := EncodeWOFF(src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files, err := Decode("x.woff", w)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		sameTables(t, name+" (fontmeta)", src, files[0].Data)
		other, err := tfont.ParseWOFF(w)
		if err != nil {
			t.Fatalf("%s: tdewolff: %v", name, err)
		}
		sameTables(t, name+" (tdewolff)", src, other)
	}
}

// buildTTC makes a collection of the given fonts (absolute table offsets).
func buildTTC(fonts ...[]byte) []byte {
	var out bytes.Buffer
	n := len(fonts)
	type tbl struct {
		tag  string
		sum  uint32
		data []byte
	}
	var parsed [][]tbl
	var flavors []uint32
	for _, f := range fonts {
		fl, ts, _ := sfntTables(f, 0)
		flavors = append(flavors, fl)
		var p []tbl
		for _, t := range ts {
			p = append(p, tbl{t.tag, t.checksum, t.data})
		}
		parsed = append(parsed, p)
	}
	head := 12 + 4*n
	dirs := 0
	for _, p := range parsed {
		dirs += 12 + 16*len(p)
	}
	dataOff := head + dirs
	putBE(&out, []byte("ttcf"), uint32(0x00010000), uint32(n))
	off := head
	for _, p := range parsed {
		binary.Write(&out, binary.BigEndian, uint32(off))
		off += 12 + 16*len(p)
	}
	var data bytes.Buffer
	for i, p := range parsed {
		putBE(&out, flavors[i], uint16(len(p)), uint16(0), uint16(0), uint16(0))
		for _, t := range p {
			putBE(&out, []byte(t.tag), t.sum, uint32(dataOff+data.Len()), uint32(len(t.data)))
			data.Write(t.data)
			data.Write(make([]byte, pad4(len(t.data))-len(t.data)))
		}
	}
	out.Write(data.Bytes())
	return out.Bytes()
}

func TestExtractFace(t *testing.T) {
	var list [][]byte
	for _, f := range testFonts(t) {
		list = append(list, f)
	}
	if len(list) < 2 {
		list = append(list, list[0])
	}
	ttc := buildTTC(list[0], list[1])
	for i := range 2 {
		face, err := ExtractFace(ttc, i)
		if err != nil {
			t.Fatal(err)
		}
		sameTables(t, "face", list[i], face)
		if _, err := Decode("face.ttf", face); err != nil {
			t.Fatalf("extracted face %d doesn't decode: %v", i, err)
		}
		w2, err := EncodeWOFF2(ttc, i)
		if err != nil {
			t.Fatal(err)
		}
		other, err := tfont.ParseWOFF2(w2)
		if err != nil {
			t.Fatal(err)
		}
		sameTables(t, "face as WOFF2", list[i], other)
	}
	if _, err := ExtractFace(ttc, 2); err == nil {
		t.Fatal("face 2 doesn't exist")
	}
	if _, err := EncodeWOFF2([]byte("%!PS-AdobeFont-1.0"), 0); err == nil {
		t.Fatal("Type 1 isn't a web font")
	}
}

func TestWeightRange(t *testing.T) {
	for _, src := range testFonts(t) {
		if _, _, ok := WeightRange(src, 0); ok {
			t.Fatal("static font reported as variable")
		}
	}
	b := readFixture(t, "HankenGrotesk-wght.ttf")
	lo, hi, ok := WeightRange(b, 0)
	if !ok || lo >= hi {
		t.Fatalf("%d–%d %v", lo, hi, ok)
	}
	if lo != 100 || hi != 900 {
		t.Fatalf("Hanken Grotesk: %d–%d", lo, hi)
	}
}
