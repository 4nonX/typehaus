package fontmeta

import "testing"

func TestFingerprint(t *testing.T) {
	ttf := readFixture(t, "Poppins-Regular.ttf")
	fp := func(name string, b []byte) string {
		t.Helper()
		f := decodeOne(t, name, b)
		if len(f.Faces) != 1 || f.Faces[0].Fingerprint == "" {
			t.Fatalf("%s: no fingerprint", name)
		}
		return f.Faces[0].Fingerprint
	}
	base := fp("Poppins-Regular.ttf", ttf)
	// the same font in other files: identical
	for _, n := range []string{"Poppins-Regular.woff", "Poppins-Regular.woff2"} {
		if got := fp(n, readFixture(t, n)); got != base {
			t.Errorf("%s: fingerprint %s, want the TTF's %s", n, got, base)
		}
	}
	// the same family name with another weight class: different
	heavier := append([]byte(nil), ttf...)
	num := int(be16(heavier, 4))
	for i := 0; i < num; i++ {
		rec := 12 + 16*i
		if string(heavier[rec:rec+4]) == "OS/2" {
			off := int(be32(heavier, rec+8))
			heavier[off+4], heavier[off+5] = 0x02, 0xBC // usWeightClass 700
		}
	}
	if fp("Poppins-Bold-ish.ttf", heavier) == base {
		t.Error("a different weight must not share the fingerprint")
	}
	// the same names and glyphs from another vendor: different
	other := append([]byte(nil), ttf...)
	for i := 0; i < num; i++ {
		rec := 12 + 16*i
		if string(other[rec:rec+4]) == "OS/2" {
			off := int(be32(other, rec+8))
			copy(other[off+58:off+62], "ZZZZ")
		}
	}
	if fp("Poppins-Regular-other.ttf", other) == base {
		t.Error("another vendor's font must not share the fingerprint")
	}
}
