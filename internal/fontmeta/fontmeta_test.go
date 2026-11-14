package fontmeta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSystemFonts(t *testing.T) {
	var files []string
	for _, pat := range []string{
		"/usr/share/fonts/truetype/dejavu/*.ttf",
		"/usr/share/fonts/opentype/*/*.otf",
		"/usr/share/fonts/truetype/*/*.ttc",
	} {
		m, _ := filepath.Glob(pat)
		if len(m) > 3 {
			m = m[:3]
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Skip("no system fonts available")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		info, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for _, face := range info.Faces {
			if face.Family == "" || face.Style == "" {
				t.Errorf("%s: missing names: %+v", f, face)
			}
		}
		t.Logf("%s → %s %+v", filepath.Base(f), info.Format, info.Faces[0].FullName)
	}
}

func TestRejects(t *testing.T) {
	if _, err := Parse([]byte("wOF2aaaaaaaaaaaaaaaa")); err != ErrWebFont {
		t.Fatalf("want ErrWebFont, got %v", err)
	}
	if _, err := Parse([]byte("hello world, not a font")); err != ErrUnsupported {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	if _, err := Parse([]byte("ttcf\x00\x01\x00\x00\x00\x00\x00\x05")); err == nil {
		t.Fatal("expected error for truncated ttc")
	}
}

func TestClassify(t *testing.T) {
	os2 := func(class byte, panose ...byte) []byte {
		b := make([]byte, 64)
		b[30] = class
		copy(b[32:], panose)
		return b
	}
	for want, t2 := range map[string][]byte{
		"serif":       os2(1),
		"slab-serif":  os2(5),
		"sans-serif":  os2(8),
		"display":     os2(9),
		"handwriting": os2(10),
		"symbol":      os2(12),
		"monospace":   os2(5, 0, 15, 4, 9), // class says slab, PANOSE monospaced (FreeMono)
		"":            os2(0),
	} {
		if got := classify(t2); got != want {
			t.Errorf("%v: got %q, want %q", t2[30:36], got, want)
		}
	}
	// PANOSE when the family class is unset
	for want, p := range map[string][]byte{
		"sans-serif": {2, 11}, "serif": {2, 2}, "slab-serif": {2, 6},
		"handwriting": {3}, "display": {4}, "symbol": {5},
	} {
		if got := classify(os2(0, p...)); got != want {
			t.Errorf("PANOSE %v: got %q, want %q", p, got, want)
		}
	}
}
