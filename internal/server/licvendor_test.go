package server

import (
	"testing"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

func TestDetectCommercial(t *testing.T) {
	cases := []struct {
		name string
		face fontmeta.Face
		want string
	}{
		{"Adobe by vendor ID", fontmeta.Face{Copyright: "Copyright © 1990 Adobe", VendorID: "ADBE"}, "Adobe"},
		{"Linotype by copyright", fontmeta.Face{Copyright: "Copyright (c) 1988 Linotype AG"}, "Linotype"},
		{"Monotype by trademark", fontmeta.Face{Trademark: "Arial is a trademark of The Monotype Corporation."}, "Monotype"},
		{"unknown foundry with an EULA", fontmeta.Face{Manufacturer: "Small Foundry",
			License: "This font is subject to the End User License Agreement."}, "Small Foundry"},
		// an open license always wins, even from a commercial foundry
		{"open license from Adobe", fontmeta.Face{VendorID: "ADBE", Copyright: "© 2010 Adobe Systems Incorporated",
			License: "This Font Software is licensed under the SIL Open Font License, Version 1.1."}, ""},
		{"says nothing", fontmeta.Face{Family: "Own Font", Copyright: "Copyright 2026 Somebody"}, ""},
	}
	for _, c := range cases {
		if got, _ := detectCommercial(c.face); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCommercialSuggestions(t *testing.T) {
	s, _, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var checked int
	s.db.QueryRow(`SELECT meta_checked FROM fonts`).Scan(&checked)
	if checked != 1 {
		t.Fatalf("upload should store vendor ID and trademark (meta_checked = %d)", checked)
	}
	// make the stored font look like an unlicensed commercial one uploaded
	// before vendor IDs were stored
	if _, err := s.db.Exec(`UPDATE fonts SET license_id = NULL, license_text = '', license_url = '',
		copyright = 'Copyright (c) 1988 Linotype AG', vendor_id = '', trademark = '', meta_checked = 0`); err != nil {
		t.Fatal(err)
	}
	var sugs []vendorSuggestion
	admin.do("GET", "/api/licenses/suggestions", nil, &sugs)
	if len(sugs) != 1 || sugs[0].Vendor != "Linotype" || sugs[0].LicenseID != nil || len(sugs[0].FontIDs) != 1 {
		t.Fatalf("suggestions: %+v", sugs)
	}
	s.db.QueryRow(`SELECT meta_checked FROM fonts`).Scan(&checked)
	if checked != 1 {
		t.Fatal("the file should have been read once and its metadata stored")
	}
	var res struct {
		Assigned int      `json:"assigned"`
		Created  []string `json:"created"`
	}
	body := map[string]any{"items": []map[string]any{{"font_ids": sugs[0].FontIDs, "vendor": "Linotype"}}}
	if st := admin.do("POST", "/api/licenses/suggestions", body, &res); st != 200 || res.Assigned != 1 ||
		len(res.Created) != 1 || res.Created[0] != "Linotype (commercial)" {
		t.Fatalf("apply: %d %+v", st, res)
	}
	var lics []licenseRow
	admin.do("GET", "/api/licenses", nil, &lics)
	if len(lics) != 2 {
		t.Fatalf("licenses: %+v", lics)
	}
	for _, l := range lics {
		if l.Name == "Linotype (commercial)" && (l.Kind != "commercial" || l.Vendor != "Linotype") {
			t.Fatalf("created license: %+v", l)
		}
	}
	// nothing left to suggest, and applying again assigns nothing
	admin.do("GET", "/api/licenses/suggestions", nil, &sugs)
	if len(sugs) != 0 {
		t.Fatalf("suggestions after apply: %+v", sugs)
	}
	admin.do("POST", "/api/licenses/suggestions", body, &res)
	if res.Assigned != 0 {
		t.Fatalf("second apply assigned %d", res.Assigned)
	}
}

func TestCountAdmins(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	fid := fonts[0].ID
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "One seat", "kind": "commercial", "seats": 1,
		"seat_unit": "users", "enforce": true}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{fid}, "license_id": lic["id"]}, nil)
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{fid}}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "anna", "password": "annapassword1"}, nil)
	anna := newClient(t, ts.URL)
	anna.do("POST", "/api/auth/login", map[string]string{"username": "anna", "password": "annapassword1"}, nil)

	// counted (the default): the admin takes the only seat, anna is refused
	admin.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fid}}, nil)
	if st := anna.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fid}}, nil); st == 200 {
		t.Fatalf("admin should hold the only seat, anna got %d", st)
	}
	// not counted: the admin's activation frees the seat for anna
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"count_admins": false}, nil)
	var lics []licenseRow
	admin.do("GET", "/api/licenses", nil, &lics)
	if lics[0].Used != 0 || lics[0].CountAdmins {
		t.Fatalf("admin still counted: %+v", lics[0])
	}
	if st := anna.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fid}}, nil); st != 200 {
		t.Fatalf("anna should get the seat: %d", st)
	}
	var usage []map[string]any
	admin.do("GET", "/api/reports/usage", nil, &usage)
	if len(usage) != 1 || usage[0]["users"].(float64) != 1 || usage[0]["uncounted_admins"].(float64) != 1 {
		t.Fatalf("usage should count anna and show the admin apart: %+v", usage)
	}
}

func TestSameFontDifferentFile(t *testing.T) {
	s, _, admin := newTestServer(t)
	first := uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	if first[0]["status"] != "added" {
		t.Fatalf("first upload: %+v", first[0])
	}
	// the WOFF2 of the same font converts to a different TTF file: still the same font
	again := uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.woff2": fixture(t, "Poppins-Regular.woff2")})
	if again[0]["status"] != "duplicate" {
		t.Fatalf("same font, different file: %+v", again[0])
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM fonts`).Scan(&n)
	if n != 1 {
		t.Fatalf("library should still hold one face, has %d", n)
	}
	// an entry from before fingerprints existed is fingerprinted on demand
	s.db.Exec(`UPDATE fonts SET fingerprint = ''`)
	again = uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.woff": fixture(t, "Poppins-Regular.woff")})
	if again[0]["status"] != "duplicate" {
		t.Fatalf("older entry without fingerprint: %+v", again[0])
	}
}

func TestRefreshFamilyNames(t *testing.T) {
	s, _, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	// a font named the old way, before the re-read
	s.db.Exec(`UPDATE fonts SET family = 'Poppins Something', style = 'Odd', names_v = 0`)
	s.refreshFamilyNames()
	var fam, style string
	var v int
	s.db.QueryRow(`SELECT family, style, names_v FROM fonts`).Scan(&fam, &style, &v)
	if fam != "Poppins" || style != "Regular" || v != 1 {
		t.Fatalf("after refresh: %q / %q / %d", fam, style, v)
	}
	// runs once: a later change is not overwritten again
	s.db.Exec(`UPDATE fonts SET family = 'Renamed by hand'`)
	s.refreshFamilyNames()
	s.db.QueryRow(`SELECT family FROM fonts`).Scan(&fam)
	if fam != "Renamed by hand" {
		t.Fatalf("second refresh changed %q", fam)
	}
}

func TestManualFamily(t *testing.T) {
	s, _, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	id := fonts[0].ID
	if fonts[0].FamilyAuto != "Poppins" || fonts[0].NamesManual {
		t.Fatalf("detected names: %+v", fonts[0])
	}
	if st := admin.do("POST", "/api/fonts/family", map[string]any{"font_ids": []int64{id}, "family": "  House   Sans ", "style": "Book"}, nil); st != 200 {
		t.Fatalf("set family: %d", st)
	}
	admin.do("GET", "/api/fonts", nil, &fonts)
	if fonts[0].Family != "House Sans" || fonts[0].Style != "Book" || !fonts[0].NamesManual || fonts[0].FamilyAuto != "Poppins" {
		t.Fatalf("after move: %+v", fonts[0])
	}
	// re-reading the files keeps names set by hand
	s.db.Exec(`UPDATE fonts SET names_v = 0`)
	s.refreshFamilyNames()
	admin.do("GET", "/api/fonts", nil, &fonts)
	if fonts[0].Family != "House Sans" {
		t.Fatalf("re-read overwrote the manual family: %+v", fonts[0])
	}
	// a style name only for a single style
	if st := admin.do("POST", "/api/fonts/family", map[string]any{"font_ids": []int64{id, 999}, "family": "X", "style": "Y"}, nil); st != 400 {
		t.Fatalf("style for several fonts: %d", st)
	}
	// back to the detected names
	admin.do("POST", "/api/fonts/family", map[string]any{"font_ids": []int64{id}, "reset": true}, nil)
	admin.do("GET", "/api/fonts", nil, &fonts)
	if fonts[0].Family != "Poppins" || fonts[0].Style != "Regular" || fonts[0].NamesManual {
		t.Fatalf("after reset: %+v", fonts[0])
	}
}
