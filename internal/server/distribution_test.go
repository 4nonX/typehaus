package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func manifestFontIDs(t *testing.T, ts *httptest.Server, tok string) map[int64]bool {
	t.Helper()
	_, m := getManifest(t, ts, tok)
	out := map[int64]bool{}
	for _, f := range m.Fonts {
		out[f.ID] = true
	}
	return out
}

func TestInstalledCollections(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	font := fonts[0].ID

	// people and a group
	var design map[string]int64
	admin.do("POST", "/api/groups", map[string]any{"name": "Design"}, &design)
	mkUser := func(name string, groups []int64) (*client, int64) {
		var res map[string]int64
		admin.do("POST", "/api/users", map[string]any{"username": name, "password": name + "password1", "group_ids": groups}, &res)
		c := newClient(t, ts.URL)
		c.do("POST", "/api/auth/login", map[string]string{"username": name, "password": name + "password1"}, nil)
		return c, res["id"]
	}
	anna, _ := mkUser("anna", []int64{design["id"]})
	ben, benID := mkUser("ben", nil)
	annaTok, benTok := enrollDevice(t, ts, anna), enrollDevice(t, ts, ben)

	// a collection for the Design group, first only "available"
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "Brand", "group_ids": []int64{design["id"]}}, &col)
	cid := itoa(col["id"])
	admin.do("POST", "/api/collections/"+cid+"/fonts", map[string]any{"font_ids": []int64{font}}, nil)
	if manifestFontIDs(t, ts, annaTok)[font] {
		t.Fatal("available collection must not install anything by itself")
	}

	// switch to "installed automatically"
	if st := admin.do("PATCH", "/api/collections/"+cid, map[string]any{"mode": "installed"}, nil); st != 200 {
		t.Fatalf("switch mode: %d", st)
	}
	if !manifestFontIDs(t, ts, annaTok)[font] {
		t.Fatal("group member must get the font without activating it")
	}
	if manifestFontIDs(t, ts, benTok)[font] {
		t.Fatal("non-member must not get it")
	}

	// the library tells anna why it's there; switching it off keeps it
	var annaFonts []fontRow
	anna.do("GET", "/api/fonts", nil, &annaFonts)
	if !annaFonts[0].Required || annaFonts[0].Activated || strings.Join(annaFonts[0].RequiredBy, ",") != "Brand" {
		t.Fatalf("library flags: %+v", annaFonts[0])
	}
	var deact map[string]int64
	anna.do("DELETE", "/api/activations", map[string]any{"font_ids": []int64{font}}, &deact)
	if deact["kept"] != 1 || !manifestFontIDs(t, ts, annaTok)[font] {
		t.Fatalf("installed font must stay: %v", deact)
	}
	var active []fontRow
	anna.do("GET", "/api/fonts?activated=1", nil, &active)
	if len(active) != 1 {
		t.Fatalf("'active for me' must include installed fonts: %d", len(active))
	}

	// joining the group installs it, leaving removes it
	admin.do("PATCH", "/api/users/"+itoa(benID), map[string]any{"group_ids": []int64{design["id"]}}, nil)
	if !manifestFontIDs(t, ts, benTok)[font] {
		t.Fatal("new member must get the font")
	}
	admin.do("PATCH", "/api/users/"+itoa(benID), map[string]any{"group_ids": []int64{}}, nil)
	if manifestFontIDs(t, ts, benTok)[font] {
		t.Fatal("former member must lose the font")
	}

	// licenses count automatically installed fonts
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Brand desktop", "seats": 1}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{font}, "license_id": lic["id"]}, nil)
	var lics []licenseRow
	admin.do("GET", "/api/licenses", nil, &lics)
	for _, l := range lics {
		if l.ID == lic["id"] && l.Users != 1 {
			t.Fatalf("seat count with an installed collection: %+v", l)
		}
	}

	// impact preview: everyone would need 3 seats (admin, anna, ben)
	var imp impactResult
	admin.do("POST", "/api/collections/impact", map[string]any{"id": col["id"], "mode": "installed", "everyone": true}, &imp)
	if imp.Members != 3 || len(imp.Licenses) != 1 || imp.Licenses[0].Now != 1 || imp.Licenses[0].After != 3 || !imp.Licenses[0].Over {
		t.Fatalf("impact: %+v", imp)
	}
	// not enforced: allowed (the UI warns)
	if st := admin.do("PATCH", "/api/collections/"+cid, map[string]any{"everyone": true}, nil); st != 200 {
		t.Fatalf("unenforced license must not block: %d", st)
	}
	admin.do("PATCH", "/api/collections/"+cid, map[string]any{"everyone": false}, nil)

	// enforced: blocked with an explanation
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"enforce": true}, nil)
	var e map[string]string
	if st := admin.do("PATCH", "/api/collections/"+cid, map[string]any{"everyone": true}, &e); st != 409 ||
		!strings.Contains(e["error"], "Brand desktop") || !strings.Contains(e["error"], "has 1 seats") {
		t.Fatalf("enforced license must block: %d %v", st, e)
	}

	// the group grows after the fact: only the seat holder gets the font
	admin.do("PATCH", "/api/users/"+itoa(benID), map[string]any{"group_ids": []int64{design["id"]}}, nil)
	if !manifestFontIDs(t, ts, annaTok)[font] {
		t.Fatal("anna holds the seat")
	}
	if manifestFontIDs(t, ts, benTok)[font] {
		t.Fatal("ben must not get a font beyond the enforced seats")
	}

	// adding fonts to an installed collection is checked too
	var lic2 map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Tiny", "seats": 1, "enforce": true}, &lic2)
	uploadFiles(t, admin, map[string][]byte{"TypehausTestBitmap.fon": fixture(t, "TypehausTestBitmap.fon")})
	admin.do("GET", "/api/fonts", nil, &fonts)
	var other int64
	for _, f := range fonts {
		if f.ID != font {
			other = f.ID
		}
	}
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{other}, "license_id": lic2["id"]}, nil)
	if st := admin.do("POST", "/api/collections/"+cid+"/fonts", map[string]any{"font_ids": []int64{other}}, &e); st != 409 || !strings.Contains(e["error"], "Tiny") {
		t.Fatalf("adding an over-licensed font must be refused: %d %v", st, e)
	}
}

// An enforced one-seat license on a collection installed for everyone: with
// "count administrators" off, the admin uses the fonts without taking the
// seat, so the one other person gets it; with it on, the admin holds the
// seat and the collection would need two.
func TestInstalledCollectionCountAdmins(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	font := fonts[0].ID
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "One seat", "kind": "commercial", "seats": 1, "enforce": true, "count_admins": false}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{font}, "license_id": lic["id"]}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "gil", "password": "gilpassword1"}, nil)
	gil := newClient(t, ts.URL)
	gil.do("POST", "/api/auth/login", map[string]string{"username": "gil", "password": "gilpassword1"}, nil)
	adminTok, gilTok := enrollDevice(t, ts, admin), enrollDevice(t, ts, gil)

	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	cid := itoa(col["id"])
	admin.do("POST", "/api/collections/"+cid+"/fonts", map[string]any{"font_ids": []int64{font}}, nil)
	var imp impactResult
	admin.do("POST", "/api/collections/impact", map[string]any{"id": col["id"], "mode": "installed", "everyone": true}, &imp)
	if len(imp.Licenses) != 1 || imp.Licenses[0].After != 1 || imp.Licenses[0].Over {
		t.Fatalf("admin not counted: one seat is enough: %+v", imp.Licenses)
	}
	if st := admin.do("PATCH", "/api/collections/"+cid, map[string]any{"mode": "installed"}, nil); st != 200 {
		t.Fatalf("switch mode: %d", st)
	}
	if !manifestFontIDs(t, ts, adminTok)[font] || !manifestFontIDs(t, ts, gilTok)[font] {
		t.Fatal("admin (not counted) and gil (the one seat) must both get the font")
	}
	var l licenseRow
	admin.do("GET", "/api/licenses/"+itoa(lic["id"]), nil, &struct {
		License *licenseRow `json:"license"`
	}{&l})
	if l.Used != 1 {
		t.Fatalf("seats in use: %d", l.Used)
	}

	// counting admins again: two people for one seat – gil loses it
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"count_admins": true}, nil)
	if manifestFontIDs(t, ts, gilTok)[font] == manifestFontIDs(t, ts, adminTok)[font] {
		t.Fatal("only one of them may hold the single seat")
	}
	admin.do("POST", "/api/collections/impact", map[string]any{"id": col["id"], "mode": "installed", "everyone": true}, &imp)
	if imp.Licenses[0].After != 2 || !imp.Licenses[0].Over {
		t.Fatalf("admins counted: %+v", imp.Licenses)
	}
}
