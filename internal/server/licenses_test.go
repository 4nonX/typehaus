package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

func uploadDoc(t *testing.T, c *client, licID int64, name string, data []byte, shared bool) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if !shared {
		mw.WriteField("shared", "0")
	}
	fw, _ := mw.CreateFormFile("files", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/licenses/"+itoa(licID)+"/documents", &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (c *client) get(path string) (int, http.Header, string) {
	c.t.Helper()
	resp, err := c.hc.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func report(t *testing.T, ts string, tok string, installed []string) {
	t.Helper()
	c := newClient(t, ts)
	j := `{"agent_version":"0.3.0","installed":["` + strings.Join(installed, `","`) + `"]}`
	req, _ := http.NewRequest("POST", ts+"/api/agent/report", strings.NewReader(j))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestLicenseManagement(t *testing.T) {
	_, ts, admin := newTestServer(t)

	// An OFL font is recognised and gets the open license automatically.
	res := uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	if msg, _ := res[0]["message"].(string); !strings.Contains(msg, "SIL Open Font License") {
		t.Fatalf("expected the OFL to be recognised, got %q", msg)
	}
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	if len(fonts) != 1 || fonts[0].LicenseID == nil {
		t.Fatalf("font should have a license: %+v", fonts)
	}
	fontID := fonts[0].ID
	var lics []licenseRow
	admin.do("GET", "/api/licenses", nil, &lics)
	if len(lics) != 1 || lics[0].Kind != "open" || lics[0].SPDX != "OFL-1.1" || len(lics[0].Rights) == 0 {
		t.Fatalf("open license: %+v", lics)
	}
	// running detection again changes nothing
	var det map[string]any
	admin.do("POST", "/api/licenses/detect", nil, &det)
	if det["assigned"].(float64) != 0 {
		t.Fatalf("detect twice: %+v", det)
	}

	// validation
	if st := admin.do("POST", "/api/licenses", map[string]any{"name": "x", "url": "javascript:alert(1)"}, nil); st != 400 {
		t.Fatalf("javascript: URL accepted: %d", st)
	}
	if st := admin.do("POST", "/api/licenses", map[string]any{"name": "x", "expires_on": "31.12.2027"}, nil); st != 400 {
		t.Fatalf("bad date accepted: %d", st)
	}

	// A commercial license, one seat, enforced.
	var created map[string]int64
	if st := admin.do("POST", "/api/licenses", map[string]any{
		"name": "Poppins Pro desktop", "vendor": "Foundry", "kind": "commercial", "rights": []string{"desktop", "bogus"},
		"seats": 1, "seat_unit": "users", "enforce": true, "reference": "INV-42", "url": "https://foundry.example/eula",
		"purchased_on": "2026-01-15", "notes": "secret",
	}, &created); st != 201 {
		t.Fatalf("create: %d", st)
	}
	lid := created["id"]
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{fontID}, "license_id": lid}, nil)

	// documents: one shared EULA, one private invoice
	if st := uploadDoc(t, admin, lid, "EULA.pdf", []byte("%PDF-1.4 eula"), true); st != 201 {
		t.Fatalf("doc upload: %d", st)
	}
	if st := uploadDoc(t, admin, lid, "invoice.html", []byte("<script>alert(1)</script>"), false); st != 201 {
		t.Fatalf("doc upload: %d", st)
	}

	// users
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{fontID}}, nil)
	login := func(name string) *client {
		admin.do("POST", "/api/users", map[string]any{"username": name, "password": name + "password"}, nil)
		c := newClient(t, ts.URL)
		c.do("POST", "/api/auth/login", map[string]string{"username": name, "password": name + "password"}, nil)
		return c
	}
	anna, ben := login("anna"), login("ben")

	// users see the license terms and shared documents, not the private ones
	var annaLics []licenseRow
	anna.do("GET", "/api/licenses", nil, &annaLics)
	if len(annaLics) != 1 || annaLics[0].Reference != "" || annaLics[0].Notes != "" || len(annaLics[0].Documents) != 1 {
		t.Fatalf("user view of licenses: %+v", annaLics)
	}
	if !equal(annaLics[0].Rights, []string{"desktop"}) {
		t.Fatalf("rights: %v", annaLics[0].Rights)
	}
	eula := annaLics[0].Documents[0]
	st, h, body := anna.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(eula.ID))
	if st != 200 || h.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline") || body != "%PDF-1.4 eula" {
		t.Fatalf("EULA download: %d %v %q", st, h, body)
	}
	var adminLic map[string]any
	admin.do("GET", "/api/licenses/"+itoa(lid), nil, &adminLic)
	docs := adminLic["license"].(map[string]any)["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("admin sees all documents: %+v", docs)
	}
	var invoiceID int64
	for _, d := range docs {
		if d.(map[string]any)["filename"] == "invoice.html" {
			invoiceID = int64(d.(map[string]any)["id"].(float64))
		}
	}
	if st, _, _ := anna.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(invoiceID)); st != 404 {
		t.Fatalf("private document visible to user: %d", st)
	}
	if st, h, _ := admin.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(invoiceID)); st != 200 ||
		h.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("HTML documents must be downloaded, not rendered: %d %v", st, h)
	}

	// seat enforcement
	if st := anna.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fontID}}, nil); st != 200 {
		t.Fatalf("anna takes the only seat: %d", st)
	}
	var e map[string]string
	if st := ben.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fontID}}, &e); st != 409 || !strings.Contains(e["error"], "seats") {
		t.Fatalf("ben must be refused: %d %v", st, e)
	}
	if st := anna.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fontID}}, nil); st != 200 {
		t.Fatalf("re-activating an own seat: %d", st)
	}
	anna.do("DELETE", "/api/activations", map[string]any{"font_ids": []int64{fontID}}, nil)
	if st := ben.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fontID}}, nil); st != 200 {
		t.Fatalf("seat freed, ben may activate: %d", st)
	}
	admin.do("GET", "/api/licenses", nil, &lics)
	for _, l := range lics {
		if l.ID == lid && (l.Used != 1 || !l.Full || l.Over) {
			t.Fatalf("usage: %+v", l)
		}
	}

	// expiry: an enforced license that ended is removed from devices
	tok := enrollDevice(t, ts, ben)
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 1 {
		t.Fatalf("before expiry: %+v", m.Fonts)
	}
	yesterday := time.Now().AddDate(0, 0, -1).Format(dateLayout)
	admin.do("PATCH", "/api/licenses/"+itoa(lid), map[string]any{"expires_on": yesterday}, nil)
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 0 {
		t.Fatalf("expired license still installed: %+v", m.Fonts)
	}
	admin.do("PATCH", "/api/licenses/"+itoa(lid), map[string]any{"enforce": false}, nil)
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 1 {
		t.Fatalf("not enforced: expiry is only a warning: %+v", m.Fonts)
	}
	admin.do("GET", "/api/licenses", nil, &lics)
	for _, l := range lics {
		if l.ID == lid && (!l.Expired || l.status() != "expired") {
			t.Fatalf("expired flag: %+v", l)
		}
	}

	// per-device seats: one device only
	admin.do("PATCH", "/api/licenses/"+itoa(lid), map[string]any{"expires_on": "", "enforce": true, "seat_unit": "devices"}, nil)
	_, m1 := getManifest(t, ts, tok)
	if len(m1.Fonts) != 1 {
		t.Fatalf("first device gets the seat: %+v", m1.Fonts)
	}
	report(t, ts.URL, tok, []string{m1.Fonts[0].SHA256})
	tok2 := enrollDevice(t, ts, ben)
	if _, m := getManifest(t, ts, tok2); len(m.Fonts) != 0 {
		t.Fatalf("second device must not get the font: %+v", m.Fonts)
	}
	if _, m := getManifest(t, ts, tok); len(m.Fonts) != 1 {
		t.Fatalf("first device keeps its seat: %+v", m.Fonts)
	}

	// CSV report
	st, h, body = admin.get("/api/licenses/export.csv")
	if st != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/csv") || !strings.Contains(body, "Poppins Pro desktop") ||
		!strings.Contains(body, "INV-42") || !strings.Contains(body, "EULA.pdf") {
		t.Fatalf("csv: %d %s", st, body)
	}
	if st, _, _ := anna.get("/api/licenses/export.csv"); st != 403 {
		t.Fatalf("report is admin only: %d", st)
	}

	// deleting the license removes its documents; the font stays
	admin.do("DELETE", "/api/licenses/"+itoa(lid), nil, nil)
	if st, _, _ := admin.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(eula.ID)); st != 404 {
		t.Fatalf("document of deleted license: %d", st)
	}
	admin.do("GET", "/api/fonts", nil, &fonts)
	if len(fonts) != 1 || fonts[0].LicenseID != nil {
		t.Fatalf("font after license deletion: %+v", fonts)
	}
}

func equal(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestDetectOpenLicense(t *testing.T) {
	cases := map[string]string{
		"This Font Software is licensed under the SIL Open Font License, Version 1.1.": "OFL-1.1",
		"https://scripts.sil.org/OFL":                                       "OFL-1.1",
		"Licensed under the Apache License, Version 2.0":                    "Apache-2.0",
		"This font is licensed under the GNU Affero General Public License": "AGPL-3.0",
		"GNU General Public License with font exception":                    "GPL-FE",
		"Licensed under the Ubuntu Font Licence 1.0":                        "UFL-1.0",
	}
	for text, want := range cases {
		if got := detectOpenLicense(text, ""); got == nil || got.SPDX != want {
			t.Errorf("%q: got %v, want %s", text, got, want)
		}
	}
	for _, text := range []string{"", "Commercial license. See https://foundry.example/eula", "All rights reserved."} {
		if got := detectOpenLicense(text, ""); got != nil {
			t.Errorf("%q: unexpected %s", text, got.SPDX)
		}
	}
}
