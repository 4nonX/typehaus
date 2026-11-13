package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

func webGet(t *testing.T, url string, hdr map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func TestWebkits(t *testing.T) {
	s, ts, admin := newTestServer(t)
	poppins := admin.upload("Poppins-Regular.ttf", fixture(t, "Poppins-Regular.ttf")) // OFL: web use included
	bitmap := admin.upload("TypehausTestBitmap.fon", fixture(t, "TypehausTestBitmap.fon"))
	variable := admin.upload("HankenGrotesk-wght.ttf", fixture(t, "HankenGrotesk-wght.ttf"))
	_, sys := testFont(t)
	other := admin.upload("other.ttf", sys)
	var desk map[string]any
	admin.do("POST", "/api/licenses", map[string]any{"name": "Desktop only", "kind": "commercial", "rights": []string{"desktop"}}, &desk)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": other, "license_id": desk["id"]}, nil)
	all := append(append(append(append([]int64{}, poppins...), bitmap...), other...), variable...)

	// only admins manage kits; domains are cleaned up
	user := newUser(t, ts, admin, "webby", false)
	if st := user.do("GET", "/api/webkits", nil, nil); st != 403 {
		t.Fatalf("user list: %d", st)
	}
	if st := admin.do("POST", "/api/webkits", map[string]any{"name": "Site", "domains": []string{"not a domain"}}, nil); st != 400 {
		t.Fatalf("bad domain: %d", st)
	}
	var created webkitCreated
	if st := admin.do("POST", "/api/webkits", map[string]any{"name": "Website", "domains": []string{"https://www.Example.com/start", "*.brand.test", "www.example.com"},
		"font_ids": all}, &created); st != 201 || !strings.HasPrefix(created.Token, "wk_") || !strings.HasSuffix(created.CSSURL, "/webfonts/"+created.Token+".css") {
		t.Fatalf("create: %d %+v", st, created)
	}
	var kit webkitRow
	admin.do("GET", "/api/webkits/"+itoa(created.ID), nil, &kit)
	if strings.Join(kit.Domains, ",") != "www.example.com,*.brand.test" || len(kit.Fonts) != 4 {
		t.Fatalf("kit: %+v", kit)
	}
	served := 0
	for _, f := range kit.Fonts {
		if f.Served {
			served++
		} else if f.Problem == "" {
			t.Fatalf("no reason: %+v", f)
		}
	}
	if served != 2 {
		t.Fatalf("served %d: %+v", served, kit.Fonts)
	}

	css := ts.URL + "/webfonts/" + created.Token + ".css"
	st, hdr, body := webGet(t, css, map[string]string{"Referer": "https://www.example.com/about"})
	if st != 200 || hdr.Get("Content-Type") != "text/css; charset=utf-8" || hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("css: %d %v", st, hdr)
	}
	text := string(body)
	if strings.Count(text, "@font-face") != 2 || !strings.Contains(text, `font-family: "Poppins"`) || !strings.Contains(text, "font-weight: 400") ||
		!strings.Contains(text, `font-family: "Hanken Grotesk"`) || !strings.Contains(text, "font-weight: 100 900") ||
		!strings.Contains(text, "doesn't include web use") || !strings.Contains(text, "only TrueType and OpenType") {
		t.Fatalf("stylesheet:\n%s", text)
	}
	// subdomains of *.brand.test; not other sites
	if st, _, _ := webGet(t, css, map[string]string{"Referer": "https://shop.brand.test/"}); st != 200 {
		t.Fatalf("subdomain: %d", st)
	}
	if st, _, b := webGet(t, css, map[string]string{"Referer": "https://evil.example/"}); st != 403 || !strings.Contains(string(b), "evil.example") {
		t.Fatalf("other site: %d", st)
	}
	// without a referrer (privacy settings) the stylesheet still loads, uncounted
	if st, _, _ := webGet(t, css, nil); st != 200 {
		t.Fatalf("no referrer: %d", st)
	}

	// the font file, as WOFF2 and WOFF, only for allowed origins
	start := strings.Index(text[strings.Index(text, `"Poppins"`):], `url("`) + 5 + strings.Index(text, `"Poppins"`)
	file := text[start : start+strings.Index(text[start:], `"`)]
	fontURL := ts.URL + "/webfonts/" + file
	st, hdr, w2 := webGet(t, fontURL, map[string]string{"Origin": "https://www.example.com"})
	if st != 200 || hdr.Get("Content-Type") != "font/woff2" || hdr.Get("Access-Control-Allow-Origin") != "https://www.example.com" ||
		!strings.Contains(hdr.Get("Cache-Control"), "immutable") {
		t.Fatalf("woff2: %d %v", st, hdr)
	}
	if f, err := fontmeta.Decode("x.woff2", w2); err != nil || f[0].Faces[0].Family != "Poppins" {
		t.Fatalf("woff2 doesn't decode: %v", err)
	}
	st, hdr, w1 := webGet(t, strings.TrimSuffix(fontURL, "2"), map[string]string{"Origin": "https://www.example.com"})
	if st != 200 || hdr.Get("Content-Type") != "font/woff" {
		t.Fatalf("woff: %d", st)
	}
	if _, err := fontmeta.Decode("x.woff", w1); err != nil {
		t.Fatalf("woff doesn't decode: %v", err)
	}
	if st, _, _ := webGet(t, fontURL, map[string]string{"Origin": "https://evil.example"}); st != 403 {
		t.Fatalf("font for another site: %d", st)
	}
	// fonts not licensed for the web aren't served even by id
	if st, _, _ := webGet(t, ts.URL+"/webfonts/"+created.Token+"/"+itoa(other[0])+"-x.woff2", map[string]string{"Origin": "https://www.example.com"}); st != 404 {
		t.Fatalf("unlicensed font: %d", st)
	}

	// page views: two counted (www.example.com, shop.brand.test)
	s.flushViews()
	admin.do("GET", "/api/webkits/"+itoa(created.ID), nil, &kit)
	if kit.Views != 2 {
		t.Fatalf("views: %d", kit.Views)
	}
	// a limit of 1 on the font's license: over, flagged
	var detail fontDetail
	admin.do("GET", "/api/fonts/"+itoa(poppins[0]), nil, &detail)
	font := detail.Font
	admin.do("PATCH", "/api/licenses/"+itoa(*font.LicenseID), map[string]any{"web_pageviews": 1}, nil)
	var lic struct {
		License licenseRow `json:"license"`
	}
	admin.do("GET", "/api/licenses/"+itoa(*font.LicenseID), nil, &lic)
	if lic.License.WebViews != 2 || !lic.License.WebOver || lic.License.status() != "over its web page views this month" {
		t.Fatalf("license: %+v", lic.License)
	}
	// still served: going over doesn't break websites
	if st, _, _ := webGet(t, css, map[string]string{"Referer": "https://www.example.com/"}); st != 200 {
		t.Fatalf("over the limit: %d", st)
	}

	// a license that loses web use drops the font from the stylesheet
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": append(poppins, variable...), "license_id": desk["id"]}, nil)
	if _, _, b := webGet(t, css, map[string]string{"Referer": "https://www.example.com/"}); strings.Contains(string(b), "@font-face") {
		t.Fatalf("font without web rights still served:\n%s", b)
	}
	// disabled and deleted kits answer 404
	admin.do("PATCH", "/api/webkits/"+itoa(created.ID), map[string]any{"enabled": false}, nil)
	if st, _, _ := webGet(t, css, nil); st != 404 {
		t.Fatalf("disabled: %d", st)
	}
	if st := admin.do("DELETE", "/api/webkits/"+itoa(created.ID), nil, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st := admin.do("GET", "/api/webkits/"+itoa(created.ID), nil, nil); st != 404 {
		t.Fatalf("deleted: %d", st)
	}
}

func TestCleanDomain(t *testing.T) {
	for in, want := range map[string]string{"Example.com": "example.com", "https://www.example.com:8443/x": "www.example.com",
		"*.Brand.test": "*.brand.test", "localhost": "localhost", "*": "*", "": ""} {
		if got, err := cleanDomain(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"intranet", "a b.com", "user@example.com", "*"} {
		if _, err := cleanDomain(bad); err == nil && bad != "*" {
			t.Errorf("%q accepted", bad)
		}
	}
	if !domainAllowed([]string{"*.brand.test"}, "brand.test") || domainAllowed([]string{"*.brand.test"}, "evilbrand.test") {
		t.Fatal("subdomain matching")
	}
}
