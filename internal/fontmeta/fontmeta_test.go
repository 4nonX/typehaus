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
