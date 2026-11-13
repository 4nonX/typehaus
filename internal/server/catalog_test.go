package server

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// pngPixel is a 1×1 PNG.
var pngPixel, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

// fakeUpstream stands in for Fontsource, Font Squirrel, Google's repository
// and the identification services, and switches the features on.
func fakeUpstream(t *testing.T, s *Server, ts *httptest.Server) *atomic.Int32 {
	t.Helper()
	var asposePolls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fontsource/v1/fonts", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":"poppins","family":"Poppins","subsets":["latin"],"weights":[400],"styles":["normal"],"defSubset":"latin","variable":false,"category":"sans-serif","license":"OFL-1.1","type":"google"},
			{"id":"web-only","family":"Web Only","subsets":["latin"],"weights":[400,700],"styles":["normal","italic"],"defSubset":"latin","variable":{"wght":{}},"category":"display","license":"OFL-1.1","type":"other"},
			{"id":"material-icons","family":"Material Icons","type":"icons"}]`)
	})
	mux.HandleFunc("GET /cdn/poppins@latest/latin-400-normal.woff2", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "Poppins-Regular.woff2"))
	})
	mux.HandleFunc("GET /squirrel/api/fontlist/all", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"family_name":"Bitmap Test","family_urlname":"bitmap-test","foundry_name":"Test Foundry","classification":"Sans Serif","family_count":"1"},
			{"family_name":"Poppins","family_urlname":"poppins","foundry_name":"ITF","classification":"Sans Serif","family_count":"18"}]`)
	})
	mux.HandleFunc("GET /squirrel/api/familyinfo/bitmap-test", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{{"listing_image": "http://" + r.Host + "/squirrel/img.png"}})
	})
	mux.HandleFunc("GET /squirrel/img.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngPixel) })
	mux.HandleFunc("GET /squirrel/fonts/download/bitmap-test", func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create("TypehausTestBitmap.fon")
		f.Write(fixture(t, "TypehausTestBitmap.fon"))
		f, _ = zw.Create("Freeware License.txt")
		io.WriteString(f, "This font is free for personal and commercial use. Do not sell it.")
		zw.Close()
		w.Write(b.Bytes())
	})
	mux.HandleFunc("GET /google/ofl/poppins/METADATA.pb", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "name: \"Poppins\"\nlicense: \"OFL\"\nfonts {\n  name: \"Poppins\"\n  filename: \"Poppins-Regular.ttf\"\n}\n")
	})
	mux.HandleFunc("GET /google/ofl/poppins/Poppins-Regular.ttf", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "Poppins-Regular.ttf"))
	})
	mux.HandleFunc("GET /google/ofl/poppins/OFL.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "OFL.txt"))
	})
	mux.HandleFunc("POST /whatfontis/", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("API_KEY") != "wfi-key" {
			w.WriteHeader(409)
			io.WriteString(w, "No API key")
			return
		}
		if img, _ := base64.StdEncoding.DecodeString(r.Form.Get("urlimagebase64")); !bytes.Equal(img, pngPixel) {
			w.WriteHeader(422)
			io.WriteString(w, "Save Image Error")
			return
		}
		io.WriteString(w, `[{"title":"Poppins Regular","url":"https://www.whatfontis.com/Poppins.font","type":"Free"},
			{"title":"Helvetica Neue LT Pro 55 Roman","url":"https://www.whatfontis.com/HNLT.font","type":"Commercial"}]`)
	})
	mux.HandleFunc("POST /mixfont", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "mix-key" {
			w.WriteHeader(401)
			io.WriteString(w, `{"message":"invalid key"}`)
			return
		}
		var req struct {
			ImageURL string `json:"image_url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		// Mixfont fetches the image from the link typehaus gave it.
		resp, err := http.Get(req.ImageURL)
		if err != nil || resp.StatusCode != 200 {
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"could not fetch image"}`)
			return
		}
		img, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(img, pngPixel) {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"word":"Hello","font_matches":[{"name":"Poppins","score":0.91,"fonts":[]},{"name":"Bitmap Test","score":0.4}],"creditsRemaining":9}`)
	})
	mux.HandleFunc("POST /aspose/connect/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "as-id" || r.Form.Get("client_secret") != "as-secret" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		io.WriteString(w, `{"access_token":"as-token","expires_in":3600}`)
	})
	mux.HandleFunc("POST /aspose/v5.0/ocr/IdentifyFont", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer as-token" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `"task-1"`)
	})
	mux.HandleFunc("GET /aspose/v5.0/ocr/IdentifyFont", func(w http.ResponseWriter, r *http.Request) {
		if asposePolls.Add(1) == 1 {
			io.WriteString(w, `{"id":"task-1","taskStatus":"Processing","results":[]}`)
			return
		}
		data := base64.StdEncoding.EncodeToString([]byte(`{'style': 'regular', 'font': ['poppins', 'times new roman']}`))
		io.WriteString(w, `{"id":"task-1","taskStatus":"Completed","results":[{"type":"Text","data":"`+data+`"}],"error":null}`)
	})
	up := httptest.NewServer(mux)
	saved := []string{fontsourceAPI, fontsourceCDN, fontSquirrelURL, googleFontsRaw, whatFontIsURL, mixfontURL, asposeURL}
	fontsourceAPI, fontsourceCDN = up.URL+"/fontsource/v1/fonts", up.URL+"/cdn"
	fontSquirrelURL, googleFontsRaw = up.URL+"/squirrel", up.URL+"/google"
	whatFontIsURL, mixfontURL, asposeURL = up.URL+"/whatfontis/", up.URL+"/mixfont", up.URL+"/aspose"
	t.Cleanup(func() {
		up.Close()
		fontsourceAPI, fontsourceCDN, fontSquirrelURL, googleFontsRaw, whatFontIsURL, mixfontURL, asposeURL =
			saved[0], saved[1], saved[2], saved[3], saved[4], saved[5], saved[6]
	})
	s.cfg.Catalog, s.cfg.Identify, s.cfg.BaseURL = true, true, ts.URL
	checkReachable = func(string) (string, string) { return "", "" } // the stand-ins run here
	t.Cleanup(func() { checkReachable = privateAddress })
	return &asposePolls
}

func TestCatalog(t *testing.T) {
	s, ts, admin := newTestServer(t)
	fakeUpstream(t, s, ts)

	var list []catalogFamily
	if st := admin.do("GET", "/api/catalog", nil, &list); st != 200 {
		t.Fatalf("search: %d", st)
	}
	// Poppins appears once (Google wins over Font Squirrel), icons are left out.
	if len(list) != 3 {
		t.Fatalf("want 3 families, got %+v", list)
	}
	by := map[string]catalogFamily{}
	for _, f := range list {
		by[f.ID] = f
	}
	if p := by["google:poppins"]; !p.Importable || p.License != "OFL-1.1" || p.InLibrary || p.Page != "https://fonts.google.com/specimen/Poppins" {
		t.Fatalf("poppins: %+v", p)
	}
	if w := by["fontsource:web-only"]; w.Importable || w.Reason == "" || !w.Variable || w.Styles != 4 {
		t.Fatalf("web only: %+v", w)
	}
	if b := by["fontsquirrel:bitmap-test"]; !b.Importable || b.License != "Freeware" || b.Foundry != "Test Foundry" {
		t.Fatalf("bitmap: %+v", b)
	}
	admin.do("GET", "/api/catalog?q=pop", nil, &list)
	if len(list) != 1 || list[0].ID != "google:poppins" {
		t.Fatalf("q=pop: %+v", list)
	}
	admin.do("GET", "/api/catalog?source=fontsquirrel", nil, &list)
	if len(list) != 1 {
		t.Fatalf("source filter: %+v", list)
	}

	// previews come through typehaus
	resp, _ := admin.hc.Get(ts.URL + "/api/catalog/preview/google:poppins")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "font/woff2" {
		t.Fatalf("font preview: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()
	resp, _ = admin.hc.Get(ts.URL + "/api/catalog/preview/fontsquirrel:bitmap-test")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(b, pngPixel) {
		t.Fatalf("image preview: %d", resp.StatusCode)
	}

	// users can search but not import
	user := newUser(t, ts, admin, "cat", false)
	if st := user.do("GET", "/api/catalog", nil, &list); st != 200 {
		t.Fatalf("user search: %d", st)
	}
	if st := user.do("POST", "/api/catalog/import", map[string]any{"ids": []string{"google:poppins"}}, nil); st != 403 {
		t.Fatalf("user import: %d", st)
	}

	var res []catalogImportResult
	if st := admin.do("POST", "/api/catalog/import", map[string]any{"ids": []string{"google:poppins", "fontsquirrel:bitmap-test", "fontsource:web-only", "nope:x"}}, &res); st != 200 {
		t.Fatalf("import: %d", st)
	}
	if res[0].Status != "added" || res[0].Added != 1 || len(res[0].Licenses) != 1 || res[0].Licenses[0] != "SIL Open Font License 1.1" {
		t.Fatalf("google import: %+v", res[0])
	}
	if res[1].Status != "added" || res[1].Added < 1 || len(res[1].Licenses) != 1 || !strings.Contains(res[1].Licenses[0], "Font Squirrel freeware") {
		t.Fatalf("squirrel import: %+v", res[1])
	}
	if res[2].Status != "error" || res[3].Status != "error" {
		t.Fatalf("refused imports: %+v %+v", res[2], res[3])
	}
	// the freeware license holds the license text for review
	var lics []map[string]any
	admin.do("GET", "/api/licenses", nil, &lics)
	var free map[string]any
	for _, l := range lics {
		if strings.Contains(l["name"].(string), "Font Squirrel") {
			free = l
		}
	}
	if free == nil || free["vendor"] != "Test Foundry" || free["rights"] != nil && len(free["rights"].([]any)) != 0 {
		t.Fatalf("freeware license: %+v", free)
	}
	var detail struct {
		License struct {
			Documents []map[string]any `json:"documents"`
		} `json:"license"`
	}
	admin.do("GET", "/api/licenses/"+itoa(int64(free["id"].(float64))), nil, &detail)
	if docs := detail.License.Documents; len(docs) != 1 || docs[0]["filename"] != "Freeware License.txt" || docs[0]["kind"] != "eula" {
		t.Fatalf("freeware documents: %+v", docs)
	}
	admin.do("GET", "/api/catalog?q=poppins", nil, &list)
	if !list[0].InLibrary {
		t.Fatalf("imported family not marked: %+v", list[0])
	}
	if st := admin.do("POST", "/api/catalog/import", map[string]any{"ids": []string{"google:poppins"}}, &res); st != 200 || res[0].Status != "exists" {
		t.Fatalf("second import: %d %+v", st, res)
	}

	// switched off
	s.cfg.Catalog = false
	var e apiErrorBody
	if st := admin.do("GET", "/api/catalog", nil, &e); st != 404 || e.Code != "feature_disabled" {
		t.Fatalf("disabled: %d %+v", st, e)
	}
}

func identifyCall(t *testing.T, c *client, base string, image []byte, link string) (int, identifyResponse, apiErrorBody) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if image != nil {
		fw, _ := mw.CreateFormFile("image", "shot.png")
		fw.Write(image)
	}
	if link != "" {
		mw.WriteField("url", link)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", base+"/api/identify", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Typehaus", "1")
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out identifyResponse
	var e apiErrorBody
	json.Unmarshal(b, &out)
	json.Unmarshal(b, &e)
	return resp.StatusCode, out, e
}

func TestIdentify(t *testing.T) {
	s, ts, admin := newTestServer(t)
	fakeUpstream(t, s, ts)
	user := newUser(t, ts, admin, "designer", false)

	// no keys yet
	if st, _, e := identifyCall(t, user, ts.URL, pngPixel, ""); st != 409 || e.Code != "not_configured" {
		t.Fatalf("unconfigured: %d %+v", st, e)
	}
	// only admins see and set keys; secrets are never shown again
	if st := user.do("GET", "/api/services", nil, nil); st != 403 {
		t.Fatalf("user services: %d", st)
	}
	for id, v := range map[string]map[string]string{
		"whatfontis": {"key": "wfi-key"}, "mixfont": {"key": "mix-key"},
		"aspose": {"client_id": "as-id", "client_secret": "as-secret"},
	} {
		if st := admin.do("PUT", "/api/services/"+id, v, nil); st != 204 {
			t.Fatalf("set %s: %d", id, st)
		}
	}
	var svc []serviceState
	admin.do("GET", "/api/services", nil, &svc)
	raw, _ := json.Marshal(svc)
	if strings.Contains(string(raw), "wfi-key") || strings.Contains(string(raw), "as-secret") || !strings.Contains(string(raw), `"hint":"as-id"`) {
		t.Fatalf("secrets leak or plain value missing: %s", raw)
	}
	for _, x := range svc {
		if !x.Configured {
			t.Fatalf("%s not configured", x.ID)
		}
	}

	// a library font and a catalog family to match against
	admin.upload("Poppins-Regular.ttf", fixture(t, "Poppins-Regular.ttf"))
	var col map[string]any
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	admin.do("POST", "/api/collections/"+itoa(int64(col["id"].(float64)))+"/fonts", map[string]any{"font_ids": []int64{fonts[0].ID}}, nil)

	st, out, e := identifyCall(t, user, ts.URL, pngPixel, "")
	if st != 200 {
		t.Fatalf("identify: %d %+v", st, e)
	}
	got := map[string]identifyServiceResult{}
	for _, r := range out.Results {
		got[r.Service] = r
	}
	w := got["whatfontis"]
	if w.Status != "ok" || len(w.Matches) != 2 || w.Matches[0].Library == nil || w.Matches[0].Library.Family != "Poppins" ||
		w.Matches[0].Free == nil || !*w.Matches[0].Free || w.Matches[1].Library != nil || w.Matches[1].Catalog != nil {
		t.Fatalf("whatfontis: %+v", w)
	}
	m := got["mixfont"]
	if m.Status != "ok" || m.Word != "Hello" || len(m.Matches) != 2 || m.Matches[0].Score != 0.91 ||
		m.Matches[1].Catalog == nil || m.Matches[1].Catalog.ID != "fontsquirrel:bitmap-test" || !m.Matches[1].Catalog.Importable {
		t.Fatalf("mixfont: %+v", m)
	}
	a := got["aspose"]
	if a.Status != "ok" || a.Style != "regular" || len(a.Matches) != 2 || a.Matches[0].Name != "Poppins" || a.Matches[1].Name != "Times New Roman" ||
		a.Matches[0].Library == nil {
		t.Fatalf("aspose: %+v", a)
	}
	// the temporary image link is gone afterwards
	if len(s.identify.images) != 0 {
		t.Fatalf("temporary images left: %d", len(s.identify.images))
	}

	// a link: Aspose needs the image itself
	st, out, _ = identifyCall(t, user, ts.URL, nil, ts.URL+"/identify-image/none")
	if st != 200 {
		t.Fatalf("by url: %d", st)
	}
	for _, r := range out.Results {
		if r.Service == "aspose" && r.Status != "skipped" {
			t.Fatalf("aspose with a link: %+v", r)
		}
		if r.Service == "whatfontis" && r.Status != "error" {
			t.Fatalf("whatfontis can't fetch a bad link: %+v", r)
		}
	}

	// not an image
	if st, _, _ := identifyCall(t, user, ts.URL, []byte("hello"), ""); st != 400 {
		t.Fatalf("text file: %d", st)
	}
	// a wrong key is reported per service
	admin.do("PUT", "/api/services/mixfont", map[string]string{"key": "wrong"}, nil)
	admin.do("PUT", "/api/services/aspose", map[string]string{"client_id": ""}, nil)
	_, out, _ = identifyCall(t, user, ts.URL, pngPixel, "")
	if len(out.Results) != 2 {
		t.Fatalf("aspose without client id still used: %+v", out.Results)
	}
	for _, r := range out.Results {
		if r.Service == "mixfont" && (r.Status != "error" || !strings.Contains(r.Message, "refused the API key")) {
			t.Fatalf("wrong key: %+v", r)
		}
	}

	// lookup of a name from a site without an API
	var lk lookupResponse
	user.do("GET", "/api/lookup?name="+url.QueryEscape("Poppins SemiBold"), nil, &lk)
	if len(lk.Library) != 1 || lk.Library[0].Family != "Poppins" || len(lk.Catalog) == 0 || lk.Catalog[0].ID != "google:poppins" || !lk.Catalog[0].InLibrary {
		t.Fatalf("lookup: %+v", lk)
	}

	// hourly limit per person
	for i := 0; i < identifyPerHour; i++ {
		s.identify.allow(99)
	}
	if s.identify.allow(99) {
		t.Fatal("limit not applied")
	}
	var info identifyInfo
	user.do("GET", "/api/identify", nil, &info)
	if !info.Enabled || len(info.Services) != 2 || len(info.Links) < 5 {
		t.Fatalf("info: %+v", info)
	}
}

func newUser(t *testing.T, ts *httptest.Server, admin *client, name string, isAdmin bool) *client {
	t.Helper()
	if st := admin.do("POST", "/api/users", map[string]any{"username": name, "password": name + "password1", "is_admin": isAdmin}, nil); st != 201 && st != 200 {
		t.Fatalf("create user: %d", st)
	}
	c := newClient(t, ts.URL)
	if st := c.do("POST", "/api/auth/login", map[string]string{"username": name, "password": name + "password1"}, nil); st != 200 {
		t.Fatalf("login %s: %d", name, st)
	}
	return c
}

func TestParseAspose(t *testing.T) {
	style, fonts := parseAsposeFonts("eydzdHlsZSc6ICdyZWd1bGFyJywgJ2ZvbnQnOiBbJ3RhaG9tYScsICd0aW1lcycsICd2ZXJkYW5hJ119")
	if style != "regular" || strings.Join(fonts, ",") != "Tahoma,Times,Verdana" {
		t.Fatalf("%q %v", style, fonts)
	}
}

func TestPrivateAddress(t *testing.T) {
	for raw, private := range map[string]bool{
		"https://typehaus.greiff.local": true,
		"http://fonts":                  true,
		"http://localhost:8080":         true,
		"http://192.168.1.20:8080":      true,
		"http://10.0.0.5":               true,
		"http://[fd00::1]":              true,
		"https://fonts.corp":            true,
		"https://intranet.home.arpa":    true,
		"https://fonts.example.com":     false,
		"https://typehaus.markt-apo.de": false,
		"http://203.0.113.9":            false, // documentation range, but not private
	} {
		_, why := privateAddress(raw)
		if (why != "") != private {
			t.Errorf("%s: %q", raw, why)
		}
	}
}

// A service answering with a web page shows a sentence, not the page source.
func TestServiceErrHTML(t *testing.T) {
	resp := &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(`<!DOCTYPE html><html lang="en"><head><meta charSet="utf-8"/></head></html>`))}
	err := serviceErr("Mixfont", resp)
	if strings.Contains(err.Error(), "<") || !strings.Contains(err.Error(), "web page") {
		t.Fatalf("%v", err)
	}
}
