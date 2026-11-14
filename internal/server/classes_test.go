package server

import "testing"

func TestFontClasses(t *testing.T) {
	s, _, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{
		"Poppins-Regular.ttf":    fixture(t, "Poppins-Regular.ttf"),
		"HankenGrotesk-wght.ttf": fixture(t, "HankenGrotesk-wght.ttf"),
	})
	get := func() map[string]fontRow {
		var fonts []fontRow
		admin.do("GET", "/api/fonts", nil, &fonts)
		out := map[string]fontRow{}
		for _, f := range fonts {
			out[f.Family] = f
		}
		return out
	}
	f := get()
	if !f["Hanken Grotesk"].Variable || f["Poppins"].Variable || f["Poppins"].Width != 5 {
		t.Fatalf("variable/width: %+v", f)
	}
	if f["Poppins"].Class != "" {
		t.Fatalf("Poppins declares no class, got %q", f["Poppins"].Class)
	}

	// the catalog fills in fonts that declare nothing; a declared or
	// different family is left alone
	s.applyCatalogClasses([]*catalogEntry{
		{ID: "google:poppins", Family: "Poppins", Category: "sans-serif"},
		{ID: "google:other", Family: "Other", Category: "serif"},
		{ID: "x:odd", Family: "Hanken Grotesk", Category: "weird"},
	})
	if f = get(); f["Poppins"].Class != "sans-serif" || f["Hanken Grotesk"].Class != "" {
		t.Fatalf("catalog classes: %q %q", f["Poppins"].Class, f["Hanken Grotesk"].Class)
	}
	// imported from the catalog: its category wins over the font's own
	s.db.Exec(`UPDATE fonts SET catalog_ref = 'google:hanken', class = 'serif' WHERE family = 'Hanken Grotesk'`)
	s.applyCatalogClasses([]*catalogEntry{{ID: "google:hanken", Family: "Hanken Grotesk", Category: "sans-serif"}})
	if f = get(); f["Hanken Grotesk"].Class != "sans-serif" {
		t.Fatalf("imported: %q", f["Hanken Grotesk"].Class)
	}

	// fonts stored before these fields existed are read once
	s.db.Exec(`UPDATE fonts SET class = '', variable = 0, width = 0, class_v = 0`)
	s.refreshClasses()
	f = get()
	if !f["Hanken Grotesk"].Variable || f["Poppins"].Width != 5 {
		t.Fatalf("backfill: %+v", f)
	}
	var left int
	s.db.QueryRow(`SELECT COUNT(*) FROM fonts WHERE class_v = 0`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d fonts not marked done", left)
	}
}
