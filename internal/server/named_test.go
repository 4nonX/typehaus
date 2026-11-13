package server

import (
	"strings"
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

// A license for named people: only they may switch the fonts on, download
// them or get them installed by a collection – enforced or not.
func TestNamedLicense(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	font := fonts[0].ID

	people := map[string]*client{}
	ids := map[string]int64{}
	for _, n := range []string{"alice", "bob"} {
		var r map[string]int64
		admin.do("POST", "/api/users", map[string]any{"username": n, "password": n + "password1"}, &r)
		ids[n] = r["id"]
		c := newClient(t, ts.URL)
		c.do("POST", "/api/auth/login", map[string]string{"username": n, "password": n + "password1"}, nil)
		people[n] = c
	}
	// everyone sees the font, and a collection installs it for everyone
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true, "mode": "installed"}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{font}}, nil)

	// more people than seats is refused
	var lic map[string]int64
	if st := admin.do("POST", "/api/licenses", map[string]any{"name": "Named", "seats": 1, "licensed_to": []int64{ids["alice"], ids["bob"]}}, nil); st != 400 {
		t.Fatalf("2 people on 1 seat: %d", st)
	}
	if st := admin.do("POST", "/api/licenses", map[string]any{"name": "Named", "seats": 2, "downloadable": true,
		"licensed_to": []int64{ids["alice"], 99999}}, nil); st != 400 {
		t.Fatalf("unknown user: %d", st)
	}
	if st := admin.do("POST", "/api/licenses", map[string]any{"name": "Named", "seats": 2, "downloadable": true,
		"licensed_to": []int64{ids["alice"]}}, &lic); st != 201 {
		t.Fatalf("create: %d", st)
	}
	lid := itoa(lic["id"])
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{font}, "license_id": lic["id"]}, nil)
	if st := admin.do("PATCH", "/api/licenses/"+lid, map[string]any{"seats": 0}, nil); st != 200 {
		t.Fatalf("unlimited seats: %d", st)
	}
	if st := admin.do("PATCH", "/api/licenses/"+lid, map[string]any{"seats": 1, "licensed_to": []int64{ids["alice"], ids["bob"]}}, nil); st != 400 {
		t.Fatalf("2 people on 1 seat (update): %d", st)
	}

	// what each one sees
	view := func(c *client) licenseRow {
		var ls []licenseRow
		c.do("GET", "/api/licenses", nil, &ls)
		for _, l := range ls {
			if l.ID == lic["id"] {
				return l
			}
		}
		t.Fatal("license not listed")
		return licenseRow{}
	}
	if l := view(admin); !l.NamedUsers || len(l.LicensedTo) != 1 || l.LicensedTo[0].Username != "alice" || l.LicensedToMe {
		t.Fatalf("admin view: %+v", l)
	}
	if l := view(people["alice"]); !l.NamedUsers || !l.LicensedToMe || len(l.LicensedTo) != 0 {
		t.Fatalf("alice view: %+v", l)
	}
	if l := view(people["bob"]); !l.NamedUsers || l.LicensedToMe {
		t.Fatalf("bob view: %+v", l)
	}

	// installed by the collection: for alice only
	aliceDev, bobDev := enrollDevice(t, ts, people["alice"]), enrollDevice(t, ts, people["bob"])
	if _, m := getManifest(t, ts, aliceDev); len(m.Fonts) != 1 {
		t.Fatalf("alice's computer: %+v", m.Fonts)
	}
	if _, m := getManifest(t, ts, bobDev); len(m.Fonts) != 0 {
		t.Fatalf("bob's computer got a font licensed to alice: %+v", m.Fonts)
	}
	// an all-users computer can't keep it to the named people
	var code map[string]any
	admin.do("POST", "/api/enroll-codes", nil, &code)
	var er protocol.EnrollResponse
	newClient(t, ts.URL).do("POST", "/api/agent/enroll", protocol.EnrollRequest{Code: code["code"].(string), Platform: "windows-machine"}, &er)
	admin.do("PATCH", "/api/licenses/"+lid, map[string]any{"count_admins": false}, nil)
	if _, m := getManifest(t, ts, er.Token); len(m.Fonts) != 0 {
		t.Fatalf("all-users computer: %+v", m.Fonts)
	}

	// switching on and downloading
	var e apiErrorBody
	if st := people["bob"].do("POST", "/api/activations", map[string]any{"font_ids": []int64{font}}, &e); st != 409 || e.Code != "license_refused" || !strings.Contains(e.Error, "named people") {
		t.Fatalf("bob activates: %d %+v", st, e)
	}
	if st := people["alice"].do("POST", "/api/activations", map[string]any{"font_ids": []int64{font}}, nil); st != 200 {
		t.Fatalf("alice activates: %d", st)
	}
	if st, _, _ := people["bob"].get("/api/fonts/download?ids=" + itoa(font)); st != 403 {
		t.Fatalf("bob downloads: %d", st)
	}
	if st, _, _ := people["alice"].get("/api/fonts/download?ids=" + itoa(font)); st != 200 {
		t.Fatalf("alice downloads: %d", st)
	}
	// an admin the license doesn't count is not held to it
	if st, _, _ := admin.get("/api/fonts/download?ids=" + itoa(font)); st != 200 {
		t.Fatalf("admin (not counted) downloads: %d", st)
	}
	// a token isn't a person
	tok := newToken(t, admin, "Docs", "read")
	if st, _, _ := apiCall(t, ts.URL, tok, "GET", "/api/v1/fonts/download?ids="+itoa(font), nil); st != 403 {
		t.Fatalf("token downloads: %d", st)
	}

	// the app's plugin API: refused for bob, with the reason
	var res protocol.ResolveResponse
	devicePost(t, ts.URL, bobDev, "/api/agent/fonts/resolve", protocol.ResolveRequest{Fonts: []protocol.FontRef{{PostScript: fonts[0].PostScriptName}}}, &res)
	if len(res.Fonts) != 1 || res.Fonts[0].Status != "refused" || !strings.Contains(res.Fonts[0].Reason, "named people") {
		t.Fatalf("bob resolve: %+v", res.Fonts)
	}

	// the CSV names them
	if _, _, csv := admin.get("/api/licenses/export.csv"); !strings.Contains(csv, "Licensed to") || !strings.Contains(csv, ",alice,") {
		t.Fatalf("csv: %s", csv)
	}

	// naming nobody makes it a license for everyone again
	admin.do("PATCH", "/api/licenses/"+lid, map[string]any{"licensed_to": []int64{}}, nil)
	if _, m := getManifest(t, ts, bobDev); len(m.Fonts) != 1 {
		t.Fatalf("bob after the names were removed: %+v", m.Fonts)
	}
	if l := view(people["bob"]); l.NamedUsers {
		t.Fatalf("still named: %+v", l)
	}
	// deleting a named person removes the name
	admin.do("PATCH", "/api/licenses/"+lid, map[string]any{"licensed_to": []int64{ids["bob"]}}, nil)
	admin.do("DELETE", "/api/users/"+itoa(ids["bob"]), nil, nil)
	if l := view(admin); l.NamedUsers {
		t.Fatalf("deleted user still named: %+v", l.LicensedTo)
	}
}
