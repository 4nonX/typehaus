package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

func sendInventory(t *testing.T, base, tok string, fonts []protocol.InventoryFont) int {
	t.Helper()
	b, _ := json.Marshal(protocol.InventoryReport{Fonts: fonts})
	req, _ := http.NewRequest("POST", base+"/api/agent/inventory", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type inventoryResp struct {
	Enabled   bool               `json:"enabled"`
	Scanned   int                `json:"devices_scanned"`
	Unmanaged int                `json:"unmanaged_files"`
	Findings  []inventoryFinding `json:"findings"`
}

// Fonts found outside typehaus: copies of licensed library families are
// findings (exact copy or another version); open-source families and fonts
// unknown to the library are not.
func TestInventory(t *testing.T) {
	s, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	poppins := fonts[0]
	tok := enrollDevice(t, ts, admin)
	report := []protocol.InventoryFont{
		{Path: "/Library/Fonts/Poppins-Regular.ttf", Scope: "computer", Family: "Poppins", Style: "Regular", SHA256: poppins.SHA256, Size: 1},
		{Path: "/Users/a/Library/Fonts/Poppins-Bold-old.otf", Scope: "user", Family: "poppins", Style: "Bold", SHA256: "aa", Size: 1},
		{Path: "/Library/Fonts/Arial.ttf", Scope: "computer", Family: "Arial", Style: "Regular", SHA256: "bb", Size: 1},
	}

	if st := sendInventory(t, ts.URL, tok, report); st != 409 {
		t.Fatalf("inventory switched off: want 409, got %d", st)
	}
	s.cfg.Inventory = true
	_, m := getManifest(t, ts, tok)
	if !m.Inventory {
		t.Fatal("manifest should ask for the inventory")
	}
	if st := sendInventory(t, ts.URL, tok, report); st != 200 {
		t.Fatalf("send: %d", st)
	}

	// Poppins is detected as OFL: copying it is fine
	var inv inventoryResp
	admin.do("GET", "/api/inventory", nil, &inv)
	if !inv.Enabled || inv.Scanned != 1 || inv.Unmanaged != 3 || len(inv.Findings) != 0 {
		t.Fatalf("open-source copies are not findings: %+v", inv)
	}

	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Poppins commercial", "kind": "commercial"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{poppins.ID}, "license_id": lic["id"]}, nil)
	admin.do("GET", "/api/inventory", nil, &inv)
	if len(inv.Findings) != 1 {
		t.Fatalf("findings: %+v", inv.Findings)
	}
	f := inv.Findings[0]
	if f.Family != "Poppins" || f.License != "Poppins commercial" || f.Devices != 1 || len(f.Copies) != 2 {
		t.Fatalf("finding: %+v", f)
	}
	exact := 0
	for _, c := range f.Copies {
		if c.Exact {
			exact++
			if c.Scope != "computer" {
				t.Fatalf("scope: %+v", c)
			}
		}
	}
	if exact != 1 {
		t.Fatalf("one exact copy expected: %+v", f.Copies)
	}

	// audit exports: one row per copy, and the count per license
	_, _, body := admin.get("/api/inventory.csv")
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil || len(recs) != 3 || recs[0][0] != "Family" {
		t.Fatalf("inventory csv: %v %q", err, body)
	}
	exactRows := 0
	for _, r := range recs[1:] {
		if r[0] != "Poppins" || r[2] != "Poppins commercial" {
			t.Fatalf("row: %q", r)
		}
		if r[6] == "yes" {
			exactRows++
		}
	}
	if exactRows != 1 {
		t.Fatalf("exact copies in csv: %d", exactRows)
	}
	_, _, body = admin.get("/api/licenses/export.csv")
	recs, _ = csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	col := -1
	for i, h := range recs[0] {
		if h == "Copies outside typehaus" {
			col = i
		}
	}
	found := false
	for _, r := range recs[1:] {
		if col >= 0 && r[0] == "Poppins commercial" {
			found = r[col] == "2"
		}
	}
	if !found {
		t.Fatalf("license csv column: %q", recs)
	}

	// a new report replaces the old one
	sendInventory(t, ts.URL, tok, report[2:])
	admin.do("GET", "/api/inventory", nil, &inv)
	if inv.Unmanaged != 1 || len(inv.Findings) != 0 {
		t.Fatalf("after re-scan: %+v", inv)
	}
	// users may not read it
	admin.do("POST", "/api/users", map[string]any{"username": "gil", "password": "gilpassword1"}, nil)
	gil := newClient(t, ts.URL)
	gil.do("POST", "/api/auth/login", map[string]string{"username": "gil", "password": "gilpassword1"}, nil)
	if st := gil.do("GET", "/api/inventory", nil, nil); st != 403 {
		t.Fatalf("non-admin: %d", st)
	}
}
