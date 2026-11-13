package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"4.004", "4.003", 1}, {"1.10", "1.9", 1}, {"2.0", "2.0.1", -1}, {"2.1;hotfix", "2.1", 0},
		{"1.000;PS 001.000;hotconv", "1.001", -1}, {"", "1.0", -1}, {"3", "3.0", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// bumpVersion returns a copy of a font whose version string from reads to
// (same length, in both the Mac and the Windows name records): the same
// style in another version.
func bumpVersion(t *testing.T, b []byte, from, to string) []byte {
	t.Helper()
	out := bytes.ReplaceAll(b, []byte(from), []byte(to))
	utf16 := func(s string) []byte {
		var r []byte
		for _, c := range s {
			r = append(r, 0, byte(c))
		}
		return r
	}
	out = bytes.ReplaceAll(out, utf16(from), utf16(to))
	if bytes.Equal(out, b) {
		t.Fatalf("fixture has no version %s", from)
	}
	return out
}

// setVendor returns a copy of a font with another OS/2 vendor ID.
func setVendor(t *testing.T, b []byte, id string) []byte {
	t.Helper()
	out := append([]byte(nil), b...)
	num := int(binary.BigEndian.Uint16(out[4:]))
	for i := 0; i < num; i++ {
		rec := 12 + 16*i
		if string(out[rec:rec+4]) == "OS/2" {
			off := int(binary.BigEndian.Uint32(out[rec+8:]))
			copy(out[off+58:off+62], id)
			return out
		}
	}
	t.Fatal("no OS/2 table")
	return nil
}

// fixtureVersion is the fixture's version and the same string one higher.
func fixtureVersion(t *testing.T, c *client) (string, string) {
	var fonts []fontRow
	c.do("GET", "/api/fonts", nil, &fonts)
	v := fonts[0].Version
	if len(v) < 5 {
		t.Fatalf("fixture version %q", v)
	}
	v = v[:5] // "4.004"
	last := v[4]
	if last == '9' {
		return v, v[:4] + "8"
	}
	return v, v[:4] + string(last+1)
}

func uploadForced(t *testing.T, c *client, name string, data []byte) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("force", "1")
	fw, _ := mw.CreateFormFile("files", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/fonts", &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Results []map[string]any `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Results
}

func TestFontUpdate(t *testing.T) {
	s, ts, admin := newTestServer(t)
	ttf := fixture(t, "Poppins-Regular.ttf")
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": ttf})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	oldID := fonts[0].ID
	from, to := fixtureVersion(t, admin)
	newer := bumpVersion(t, ttf, from, to)
	if compareVersions(to, from) <= 0 {
		t.Skip("fixture version cannot be raised")
	}

	// the old version has a license, a collection and a user who uses it
	var lic, col struct {
		ID int64 `json:"id"`
	}
	admin.do("POST", "/api/licenses", map[string]any{"name": "House"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{oldID}, "license_id": lic.ID}, nil)
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col.ID)+"/fonts", map[string]any{"font_ids": []int64{oldID}}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "eva", "password": "evapassword1"}, nil)
	eva := newClient(t, ts.URL)
	eva.do("POST", "/api/auth/login", map[string]string{"username": "eva", "password": "evapassword1"}, nil)
	eva.do("POST", "/api/activations", map[string]any{"font_ids": []int64{oldID}}, nil)

	// a newer version of the same style is added and reported as an update
	res := uploadFiles(t, admin, map[string][]byte{"Poppins-Regular-4.004.ttf": newer})
	ups, _ := res[0]["updates"].([]any)
	if res[0]["status"] != "added" || len(ups) != 1 {
		t.Fatalf("update not reported: %+v", res[0])
	}
	up := ups[0].(map[string]any)
	if up["newer"] != true || int64(up["old_id"].(float64)) != oldID {
		t.Fatalf("update pair: %+v", up)
	}
	newID := int64(up["new_id"].(float64))
	// Poppins names its open license, so the upload assigned it; clear it to
	// stand in for a commercial font, whose license is set by hand
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{newID}, "license_id": nil}, nil)

	// replace: everything moves to the new version, the old one is gone
	var r map[string]int
	admin.do("POST", "/api/fonts/replace", map[string]any{"pairs": []map[string]int64{{"old_id": oldID, "new_id": newID}}}, &r)
	if r["replaced"] != 1 {
		t.Fatalf("replace: %+v", r)
	}
	admin.do("GET", "/api/fonts", nil, &fonts)
	if len(fonts) != 1 || fonts[0].ID != newID || fonts[0].LicenseID == nil || *fonts[0].LicenseID != lic.ID {
		t.Fatalf("after replace: %+v", fonts)
	}
	var evaFonts []fontRow
	eva.do("GET", "/api/fonts", nil, &evaFonts)
	if len(evaFonts) != 1 || !evaFonts[0].Activated {
		t.Fatalf("eva should see the new version, activated: %+v", evaFonts)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM collection_fonts WHERE font_id = ?`, newID).Scan(&n)
	if n != 1 {
		t.Fatalf("collection not carried over")
	}

	// a file skipped as the same font can be uploaded anyway
	same := uploadFiles(t, admin, map[string][]byte{"Poppins-4.004-copy.ttf": append(newer, 0, 0, 0, 0)})
	if same[0]["status"] != "duplicate" || same[0]["same_font"] != true {
		t.Fatalf("same font in another file: %+v", same[0])
	}
	forced := uploadForced(t, admin, "Poppins-4.004-copy.ttf", append(newer, 0, 0, 0, 0))
	if forced[0]["status"] != "added" || forced[0]["updates"] == nil {
		t.Fatalf("forced upload: %+v", forced[0])
	}
}

// Fonts that share their names but come from another vendor are neither
// duplicates nor updates, and a license never moves across vendors.
func TestOtherVendor(t *testing.T) {
	_, _, admin := newTestServer(t)
	ttf := fixture(t, "Poppins-Regular.ttf")
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": ttf})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	oldID := fonts[0].ID
	var lic struct {
		ID int64 `json:"id"`
	}
	admin.do("POST", "/api/licenses", map[string]any{"name": "House"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{oldID}, "license_id": lic.ID}, nil)

	// same names, version and glyphs, another manufacturer and vendor ID:
	// added, not skipped
	other := bumpVersion(t, setVendor(t, ttf, "ZZZZ"), "Indian Type Foundry", "Zndian Type Foundry")
	res := uploadFiles(t, admin, map[string][]byte{"Poppins-Regular-other.ttf": other})
	if res[0]["status"] != "added" {
		t.Fatalf("another vendor's font must not be a duplicate: %+v", res[0])
	}
	ups, _ := res[0]["updates"].([]any)
	if len(ups) != 1 {
		t.Fatalf("name clash not reported: %+v", res[0])
	}
	up := ups[0].(map[string]any)
	if up["other_vendor"] != true || up["new_vendor"] == up["old_vendor"] {
		t.Fatalf("other vendor not flagged: %+v", up)
	}
	newID := int64(up["new_id"].(float64))
	// Poppins names its open license, so the upload assigned it; clear it to
	// stand in for a commercial font, whose license is set by hand
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{newID}, "license_id": nil}, nil)

	var r map[string]int
	admin.do("POST", "/api/fonts/replace", map[string]any{"pairs": []map[string]int64{{"old_id": oldID, "new_id": newID}}}, &r)
	if r["replaced"] != 1 || r["license_not_carried"] != 1 {
		t.Fatalf("replace: %+v", r)
	}
	admin.do("GET", "/api/fonts", nil, &fonts)
	if len(fonts) != 1 || fonts[0].LicenseID != nil {
		t.Fatalf("the license must not move to another vendor's font: %+v", fonts)
	}
}

func TestVendorOf(t *testing.T) {
	if vendorOf("PfEd", "") != "" || vendorOf("ITFO", "") != "ITFO" || vendorOf("ITFO", "Indian Type Foundry") != "Indian Type Foundry" {
		t.Error("vendorOf")
	}
	if otherVendor(fontOrigin{vendorID: "LINO", manufacturer: "Linotype"}, fontOrigin{vendorID: "MONO", manufacturer: "Linotype"}) {
		t.Error("the same manufacturer under a new vendor ID is the same vendor")
	}
	if !otherVendor(fontOrigin{vendorID: "LINO", manufacturer: "Linotype"}, fontOrigin{vendorID: "LINO", manufacturer: "Bitstream"}) {
		t.Error("another manufacturer")
	}
	a := fontOrigin{vendorID: "PfEd"}
	b := fontOrigin{vendorID: "ADBE"}
	if otherVendor(a, b) {
		t.Error("a placeholder vendor ID proves nothing")
	}
	if !otherVendor(fontOrigin{vendorID: "ADBE"}, fontOrigin{vendorID: "LINO"}) {
		t.Error("two registered vendor IDs that differ")
	}
}
