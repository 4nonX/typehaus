package fontmeta

import "testing"

func TestLegacyFamily(t *testing.T) {
	cases := []struct{ fam, style, wantFam, wantStyle string }{
		{"Helvetica Light", "Regular", "Helvetica", "Light"},
		{"Helvetica Light", "Italic", "Helvetica", "Light Italic"},
		{"Minion Pro Semibold", "Bold", "Minion Pro", "Semibold Bold"},
		{"Arial Black", "Regular", "Arial", "Black"},
		{"Futura Condensed Extra Bold", "Regular", "Futura", "Condensed Extra Bold"},
		// not a style word at the end: its own family
		{"Helvetica Rounded", "Bold", "Helvetica Rounded", "Bold"},
		// already a proper family and style
		{"Helvetica", "Bold", "Helvetica", "Bold"},
		// a single word is never emptied
		{"Black", "Regular", "Black", "Regular"},
		// typographic style names (not RIBBI) are left alone
		{"Foo Light", "Light Condensed", "Foo Light", "Light Condensed"},
	}
	for _, c := range cases {
		f, s := legacyFamily(c.fam, c.style)
		if f != c.wantFam || s != c.wantStyle {
			t.Errorf("%q / %q: got %q / %q, want %q / %q", c.fam, c.style, f, s, c.wantFam, c.wantStyle)
		}
	}
}
