package fontmeta

// Fuzz targets for everything that parses bytes users upload. Run one with
//
//	go test ./internal/fontmeta -run '^$' -fuzz '^FuzzDecode$' -fuzztime 5m
//
// Crashers land in testdata/fuzz/<Target>/ and then run with every
// ordinary `go test` as regression tests – commit them with the fix.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/tdewolff/font"
)

// slowParse: no input in the corpus (≤ a few hundred KB) may take longer.
// Generous, because fuzzing runs instrumented and in parallel.
const slowParse = time.Second * raceSlowdown

// fuzzSeeds is one small, valid example of every format Decode accepts,
// plus the real fonts in testdata/fonts.
func fuzzSeeds(f *testing.F) map[string][]byte {
	seeds := map[string][]byte{}
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "fonts", "*"))
	for _, p := range files {
		if filepath.Ext(p) == ".txt" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			seeds[filepath.Base(p)] = b
		}
	}
	poppins := seeds["Poppins-Regular.ttf"]
	if poppins == nil {
		f.Fatal("testdata/fonts/Poppins-Regular.ttf missing")
	}
	hanken := seeds["HankenGrotesk-wght.ttf"]
	seeds["collection.ttc"] = buildTTC(poppins, hanken)

	fnt := makeFNT("Terminal", 9, 400, false)
	seeds["terminal.fnt"] = fnt
	seeds["terminal.fon"] = makeNEFon(fnt, makeFNT("Terminal", 12, 700, true))

	rf := makeResourceFork("sfnt", poppins)
	seeds["suitcase.dfont"] = rf
	seeds["._Suitcase"] = appleDouble(rf)

	seeds["test.pfb"] = pfb("%!PS-AdobeFont-1.0: TestSerif-Bold 001.000\n/FamilyName (Test Serif) def\n/FullName (Test Serif Bold) def\n/Weight (Bold) def\n/ItalicAngle 0 def\n/FontName /TestSerif-Bold def\ncurrentfile eexec\n", []byte{0x12, 0x34, 0x56, 0x78})
	seeds["test.pfa"] = []byte("%!PS-AdobeFont-1.0: TestSans 001.000\n/FamilyName (Test Sans) def\n/FullName (Test Sans) def\n/FontName /TestSans def\ncurrentfile eexec\n0123456789abcdef\n")
	bdf := []byte("STARTFONT 2.1\nFONT -misc-test-bold-i-normal--10-100-75-75-c-60-iso10646-1\nSIZE 10 75 75\nSTARTPROPERTIES 3\nFAMILY_NAME \"Test\"\nWEIGHT_NAME \"Bold\"\nSLANT \"I\"\nENDPROPERTIES\nCHARS 0\nENDFONT\n")
	seeds["test.bdf"] = bdf
	seeds["test.bdf.gz"] = gz(bdf)
	seeds["test.pcf"] = pcf(map[string]string{"FAMILY_NAME": "Test", "WEIGHT_NAME": "Medium", "SLANT": "R", "PIXEL_SIZE": "10"})
	seeds["test.pcf.gz"] = gz(seeds["test.pcf"])
	return seeds
}

func pfb(clear string, binary_ []byte) []byte {
	var b bytes.Buffer
	seg := func(kind byte, d []byte) {
		b.Write([]byte{0x80, kind})
		binary.Write(&b, binary.LittleEndian, uint32(len(d)))
		b.Write(d)
	}
	seg(1, []byte(clear))
	seg(2, binary_)
	b.Write([]byte{0x80, 3})
	return b.Bytes()
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// appleDouble wraps a resource fork the way macOS zips it (._Name).
func appleDouble(rf []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(0x00051607))
	binary.Write(&b, binary.BigEndian, uint32(0x00020000))
	b.Write(make([]byte, 16))
	binary.Write(&b, binary.BigEndian, uint16(1))
	binary.Write(&b, binary.BigEndian, uint32(2)) // resource fork
	binary.Write(&b, binary.BigEndian, uint32(26+12))
	binary.Write(&b, binary.BigEndian, uint32(len(rf)))
	b.Write(rf)
	return b.Bytes()
}

// pcf builds a PCF file with only a properties table.
func pcf(props map[string]string) []byte {
	var strs bytes.Buffer
	type prop struct{ name, value int }
	var ps []prop
	for k, v := range props {
		n := strs.Len()
		strs.WriteString(k + "\x00")
		vo := strs.Len()
		strs.WriteString(v + "\x00")
		ps = append(ps, prop{n, vo})
	}
	var t bytes.Buffer
	binary.Write(&t, binary.LittleEndian, uint32(0)) // format: LSB byte order
	binary.Write(&t, binary.LittleEndian, uint32(len(ps)))
	for _, p := range ps {
		binary.Write(&t, binary.LittleEndian, uint32(p.name))
		t.WriteByte(1) // isString
		binary.Write(&t, binary.LittleEndian, uint32(p.value))
	}
	for t.Len()%4 != 0 {
		t.WriteByte(0)
	}
	binary.Write(&t, binary.LittleEndian, uint32(strs.Len()))
	t.Write(strs.Bytes())
	var b bytes.Buffer
	b.WriteString("\x01fcp")
	binary.Write(&b, binary.LittleEndian, uint32(1))
	binary.Write(&b, binary.LittleEndian, uint32(1)) // PCF_PROPERTIES
	binary.Write(&b, binary.LittleEndian, uint32(0))
	binary.Write(&b, binary.LittleEndian, uint32(t.Len()))
	binary.Write(&b, binary.LittleEndian, uint32(8+16))
	b.Write(t.Bytes())
	return b.Bytes()
}

// FuzzDecode: whatever a user uploads, Decode returns files or an error –
// it never panics, and every font file it returns parses.
func FuzzDecode(f *testing.F) {
	for name, b := range fuzzSeeds(f) {
		f.Add(name, b)
	}
	f.Fuzz(func(t *testing.T, name string, b []byte) {
		start := time.Now()
		files, err := Decode(name, b)
		if d := time.Since(start); d > slowParse {
			t.Fatalf("%s: Decode of %d bytes took %v – an upload must not tie up the server", name, len(b), d)
		}
		if err != nil {
			return
		}
		for _, fl := range files {
			if len(fl.Data) == 0 {
				t.Fatalf("%s: empty file %q", name, fl.Name)
			}
			if fl.Kind == KindSFNT {
				if _, err := Parse(fl.Data); err != nil {
					t.Fatalf("%s: Decode returned sfnt that Parse rejects: %v", name, err)
				}
			}
		}
	})
}

// FuzzParse: the OpenType reader on arbitrary bytes.
func FuzzParse(f *testing.F) {
	for _, b := range fuzzSeeds(f) {
		if isSFNT(b) {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := Parse(b)
		if err != nil {
			return
		}
		if len(info.Faces) == 0 {
			t.Fatal("Parse succeeded with no faces")
		}
	})
}

// FuzzWebfont: web kits serve library fonts as WOFF/WOFF2. The encoders
// never panic, and what they produce another decoder reads.
func FuzzWebfont(f *testing.F) {
	for _, b := range fuzzSeeds(f) {
		if isSFNT(b) {
			f.Add(b, 0)
			f.Add(b, 1)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte, index int) {
		start := time.Now()
		defer func() {
			if d := time.Since(start); d > 3*slowParse {
				t.Fatalf("encoding %d bytes took %v", len(b), d)
			}
		}()
		ExtractFace(b, index)
		WeightRange(b, index)
		if w, err := EncodeWOFF(b, index); err == nil {
			if _, err := font.ToSFNT(w); err != nil {
				t.Fatalf("WOFF we encoded doesn't decode: %v", err)
			}
		}
		if w, err := EncodeWOFF2(b, index); err == nil {
			// exact: every table comes back byte for byte
			if err := woff2RoundTrip(b, index, w); err != nil {
				t.Fatalf("WOFF2 we encoded doesn't round-trip: %v", err)
			}
			// and an independent decoder reads it – except for its check of
			// untransformed loca, which reads the first glyph's bounding
			// box as a transformed-glyf header (tdewolff/font)
			if _, err := font.ToSFNT(w); err != nil && err.Error() != "loca: invalid value for origLength" {
				t.Fatalf("WOFF2 we encoded doesn't decode: %v", err)
			}
		}
	})
}

// FuzzPFMFace: Type 1 metrics files uploaded next to .pfb/.pfa.
func FuzzPFMFace(f *testing.F) {
	pfm := make([]byte, 0x80)
	binary.LittleEndian.PutUint16(pfm[0:], 0x100)
	binary.LittleEndian.PutUint16(pfm[0x53:], 700)
	binary.LittleEndian.PutUint32(pfm[0x69:], 0x75)
	copy(pfm[0x75:], "Test\x00")
	f.Add(pfm)
	f.Fuzz(func(t *testing.T, b []byte) {
		PFMFace(b)
	})
}

// A gzip that unpacks to a gzip (in the extreme, to itself) is refused
// after one layer instead of being unpacked again and again.
func TestDecodeNestedGzip(t *testing.T) {
	bdf := []byte("STARTFONT 2.1\nFONT -misc-test-medium-r-normal--10-100-75-75-c-60-iso10646-1\nENDFONT\n")
	if _, err := Decode("x.bdf.gz", gz(bdf)); err != nil {
		t.Fatalf("one layer: %v", err)
	}
	if _, err := Decode("x.bdf.gz.gz", gz(gz(bdf))); err == nil || !strings.Contains(err.Error(), "inside a compressed file") {
		t.Fatalf("two layers: %v", err)
	}
}

// woff2RoundTrip decodes our WOFF2 (null transforms only, as EncodeWOFF2 writes
// it) and compares each table with the font's: identical, except head's
// flags bit 11, which the encoder sets.
func woff2RoundTrip(src []byte, index int, w []byte) error {
	_, want, err := sfntTables(src, index)
	if err != nil {
		return err
	}
	if len(w) < 48 || string(w[:4]) != "wOF2" {
		return errors.New("no WOFF2 header")
	}
	n := int(binary.BigEndian.Uint16(w[12:]))
	compLen := int(binary.BigEndian.Uint32(w[20:]))
	if n != len(want) {
		return fmt.Errorf("%d tables, want %d", n, len(want))
	}
	p := 48
	type entry struct {
		tag    string
		length int
	}
	var dir []entry
	for i := 0; i < n; i++ {
		flags := w[p]
		p++
		tag := ""
		if flags&63 == 63 {
			tag = string(w[p : p+4])
			p += 4
		} else {
			tag = woff2KnownTags[flags&63]
		}
		v, k := 0, 0
		for {
			c := w[p]
			p++
			v = v<<7 | int(c&0x7f)
			if k++; c&0x80 == 0 || k == 5 {
				break
			}
		}
		if tr := flags >> 6; (tag == "glyf" || tag == "loca") != (tr == 3) || (tag != "glyf" && tag != "loca" && tr != 0) {
			return fmt.Errorf("%s: transform version %d", tag, tr)
		}
		dir = append(dir, entry{tag, v})
	}
	raw, err := io.ReadAll(brotli.NewReader(bytes.NewReader(w[p : p+compLen])))
	if err != nil {
		return fmt.Errorf("brotli: %v", err)
	}
	off := 0
	for i, e := range dir {
		if e.tag != want[i].tag {
			return fmt.Errorf("table %d is %q, want %q", i, e.tag, want[i].tag)
		}
		if off+e.length > len(raw) {
			return fmt.Errorf("%s: past the end", e.tag)
		}
		got := raw[off : off+e.length]
		off += e.length
		exp := want[i].data
		if e.tag == "head" {
			exp = append([]byte{}, exp...)
			binary.BigEndian.PutUint16(exp[16:], binary.BigEndian.Uint16(exp[16:])|1<<11)
		}
		if !bytes.Equal(got, exp) {
			return fmt.Errorf("%s differs", e.tag)
		}
	}
	if off != len(raw) {
		return fmt.Errorf("%d bytes left over", len(raw)-off)
	}
	return nil
}

// A TrueType font whose glyph index doesn't add up gets no web font.
func TestGlyphIndexChecked(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", "Poppins-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeWOFF2(b, 0); err != nil {
		t.Fatalf("intact font: %v", err)
	}
	_, tables, _ := sfntTables(b, 0)
	var headOff int
	n := int(binary.BigEndian.Uint16(b[4:]))
	for i := 0; i < n; i++ {
		r := b[12+16*i:]
		if string(r[:4]) == "head" {
			headOff = int(binary.BigEndian.Uint32(r[8:]))
		}
	}
	_ = tables
	bad := append([]byte{}, b...)
	binary.BigEndian.PutUint16(bad[headOff+50:], 2) // indexToLocFormat
	for name, enc := range map[string]func([]byte, int) ([]byte, error){"WOFF": EncodeWOFF, "WOFF2": EncodeWOFF2} {
		if _, err := enc(bad, 0); err == nil || !strings.Contains(err.Error(), "glyph index format is 2") {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad = append([]byte{}, b...)
	binary.BigEndian.PutUint16(bad[headOff+50:], 1) // claims long offsets: loca too short
	if _, err := EncodeWOFF2(bad, 0); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("short loca: %v", err)
	}
}
