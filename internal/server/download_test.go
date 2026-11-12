package server

import (
	"archive/zip"
	"bytes"
	"sort"
	"strings"
	"testing"
)

func TestDownloads(t *testing.T) {
	s, ts, admin := newTestServer(t)
	poppins := fixture(t, "Poppins-Regular.ttf")
	pfa := []byte("%!PS-AdobeFont-1.0: Demo-Regular 001.000\n/FamilyName (Demo) readonly def\n/FullName (Demo Regular) readonly def\n/Weight (Regular) readonly def\n/FontName /Demo-Regular def\n")
	uploadFiles(t, admin, map[string][]byte{
		"Poppins-Regular.ttf": poppins,
		"Demo-Regular.pfa":    pfa,
		"Demo-Regular.pfm":    make([]byte, 200),
		"Demo-Regular.afm":    []byte("StartFontMetrics 4.1\nFontName Demo-Regular\nEndFontMetrics\n"),
	})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	byFam := map[string]int64{}
	var all []int64
	for _, f := range fonts {
		byFam[f.Family] = f.ID
		all = append(all, f.ID)
	}
	var col map[string]int64
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": all}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "eva", "password": "evapassword1"}, nil)
	eva := newClient(t, ts.URL)
	eva.do("POST", "/api/auth/login", map[string]string{"username": "eva", "password": "evapassword1"}, nil)

	// one plain font: the file itself
	st, h, body := eva.get("/api/fonts/download?ids=" + itoa(byFam["Poppins"]))
	if st != 200 || body != string(poppins) || !strings.Contains(h.Get("Content-Disposition"), "Poppins-Regular.ttf") {
		t.Fatalf("single download: %d %v (%d bytes)", st, h, len(body))
	}
	// the old preview URL with download=1 follows the same rules
	if st, _, body := eva.get("/api/fonts/" + itoa(byFam["Poppins"]) + "/file?download=1"); st != 200 || body != string(poppins) {
		t.Fatalf("file?download=1: %d", st)
	}

	// Type 1: a ZIP with the metrics files under the font's name
	st, h, body = eva.get("/api/fonts/download?ids=" + itoa(byFam["Demo"]))
	if st != 200 || h.Get("Content-Type") != "application/zip" {
		t.Fatalf("type 1 download: %d %v", st, h)
	}
	if got := zipNames(t, body); strings.Join(got, ",") != "Demo-Regular.afm,Demo-Regular.pfa,Demo-Regular.pfm" {
		t.Fatalf("type 1 zip: %v", got)
	}

	// several families: one folder each, with the license text and the
	// license's shared documents (not the private ones)
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Demo EULA"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{byFam["Demo"]}, "license_id": lic["id"]}, nil)
	uploadDoc(t, admin, lic["id"], "EULA.pdf", []byte("%PDF eula"), true)
	uploadDoc(t, admin, lic["id"], "Invoice.pdf", []byte("%PDF invoice"), false)
	st, h, body = eva.get("/api/fonts/download?ids=" + itoa(byFam["Poppins"]) + "," + itoa(byFam["Demo"]))
	got := zipNames(t, body)
	want := "Demo/Demo-Regular.afm,Demo/Demo-Regular.pfa,Demo/Demo-Regular.pfm,Demo/License/EULA.pdf,Poppins/LICENSE.txt,Poppins/Poppins-Regular.ttf"
	if st != 200 || strings.Join(got, ",") != want {
		t.Fatalf("multi-family zip: %d\n got %v\nwant %s", st, got, want)
	}

	// a license that forbids downloads: refused, partial download skips it
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"downloadable": false}, nil)
	if st, _, body := eva.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 403 || !strings.Contains(body, "does not allow") {
		t.Fatalf("forbidden by license: %d %s", st, body)
	}
	st, h, body = eva.get("/api/fonts/download?ids=" + itoa(byFam["Poppins"]) + "," + itoa(byFam["Demo"]))
	if st != 200 || body != string(poppins) || h.Get("X-Typehaus-Skipped") == "" {
		t.Fatalf("partial download: %d %v", st, h)
	}
	if st, _, _ := admin.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 200 {
		t.Fatalf("admins can always download: %d", st)
	}

	// enforced license with seats: only seat holders
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"downloadable": true, "enforce": true, "seats": 5}, nil)
	if st, _, body := eva.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 403 || !strings.Contains(body, "activate the font to take a free seat") {
		t.Fatalf("enforced, no seat: %d %s", st, body)
	}
	eva.do("POST", "/api/activations", map[string]any{"font_ids": []int64{byFam["Demo"]}}, nil)
	if st, _, _ := eva.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 200 {
		t.Fatalf("enforced, holding a seat: %d", st)
	}

	// fonts the user can't see
	admin.do("DELETE", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{byFam["Poppins"]}}, nil)
	if st, _, _ := eva.get("/api/fonts/download?ids=" + itoa(byFam["Poppins"])); st != 404 {
		t.Fatalf("invisible font: %d", st)
	}

	// server-wide switch
	s.cfg.Downloads = false
	if st, _, _ := eva.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 403 {
		t.Fatalf("downloads switched off: %d", st)
	}
	var me map[string]any
	eva.do("GET", "/api/me", nil, &me)
	if me["downloads"] != false {
		t.Fatalf("/api/me downloads: %v", me["downloads"])
	}
	if st, _, _ := admin.get("/api/fonts/download?ids=" + itoa(byFam["Demo"])); st != 200 {
		t.Fatalf("admins unaffected by the switch: %d", st)
	}
}

func zipNames(t *testing.T, body string) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}
