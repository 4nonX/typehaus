package server

import (
	"net/http"
	"strings"
	"testing"
)

// A preview address with the file's checksum may be cached for good; the id
// alone must be revalidated, because ids are reused after a delete.
func TestFontFileCaching(t *testing.T) {
	_, ts, admin := newTestServer(t)
	ids := admin.upload("Poppins-Regular.ttf", fixture(t, "Poppins-Regular.ttf"))
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	sha := fonts[0].SHA256
	id := itoa(ids[0])

	st, h, body := admin.get("/api/fonts/" + id + "/file?v=" + sha[:16])
	if st != 200 || !strings.Contains(h.Get("Cache-Control"), "immutable") || h.Get("ETag") != `"`+sha+`"` || len(body) < 1000 {
		t.Fatalf("with checksum: %d %v", st, h)
	}
	if _, h, _ := admin.get("/api/fonts/" + id + "/file"); strings.Contains(h.Get("Cache-Control"), "immutable") || !strings.Contains(h.Get("Cache-Control"), "no-cache") {
		t.Fatalf("id only: %v", h)
	}
	if _, h, _ := admin.get("/api/fonts/" + id + "/file?v=0000000000000000"); strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Fatalf("wrong checksum cached for good: %v", h)
	}
	// revalidation is cheap
	req, _ := http.NewRequest("GET", ts.URL+"/api/fonts/"+id+"/file", nil)
	req.Header.Set("If-None-Match", `"`+sha+`"`)
	req.Header.Set("X-Typehaus", "1")
	resp, err := admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 304 {
		t.Fatalf("revalidation: %d", resp.StatusCode)
	}

	// delete it and add another font: SQLite reuses the id
	admin.do("DELETE", "/api/fonts", map[string]any{"font_ids": ids}, nil)
	ids2 := admin.upload("HankenGrotesk-wght.ttf", fixture(t, "HankenGrotesk-wght.ttf"))
	admin.do("GET", "/api/fonts", nil, &fonts)
	if ids2[0] == ids[0] && fonts[0].SHA256 == sha {
		t.Fatal("same file?")
	}
	if ids2[0] == ids[0] {
		// the old cached address can't match the new font
		if _, h, _ := admin.get("/api/fonts/" + id + "/file"); h.Get("ETag") == `"`+sha+`"` {
			t.Fatal("reused id answers with the old checksum")
		}
	}
}
