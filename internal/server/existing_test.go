package server

import (
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

// The manifest tells the app each font's PostScript name and what to do with
// fonts already on the computer; what the app left out is shown per device.
func TestExistingFontsReported(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	admin.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fonts[0].ID}}, nil)
	dev := enrollDevice(t, ts, admin)

	_, m := getManifest(t, ts, dev)
	if len(m.Fonts) != 1 || m.Fonts[0].PostScript != "Poppins-Regular" || m.ExistingFonts != "skip" {
		t.Fatalf("manifest: %+v policy %q", m.Fonts, m.ExistingFonts)
	}
	// the setting: admins only, two values, part of the manifest
	user := newClient(t, ts.URL)
	admin.do("POST", "/api/users", map[string]any{"username": "eve", "password": "evepassword12"}, nil)
	user.do("POST", "/api/auth/login", map[string]string{"username": "eve", "password": "evepassword12"}, nil)
	if st := user.do("PUT", "/api/settings", map[string]any{"existing_fonts": "install"}, nil); st != 403 {
		t.Fatalf("user changes settings: %d", st)
	}
	if st := admin.do("PUT", "/api/settings", map[string]any{"existing_fonts": "sometimes"}, nil); st != 400 {
		t.Fatalf("bad value: %d", st)
	}
	var set map[string]string
	admin.do("PUT", "/api/settings", map[string]any{"existing_fonts": "install"}, &set)
	if _, m := getManifest(t, ts, dev); set["existing_fonts"] != "install" || m.ExistingFonts != "install" {
		t.Fatalf("setting: %v manifest %q", set, m.ExistingFonts)
	}

	// the app reports a font it left out
	devicePost(t, ts.URL, dev, "/api/agent/report", protocol.Report{AgentVersion: "1.0.0", Installed: []string{},
		Existing: []protocol.Existing{{SHA256: fonts[0].SHA256, FullName: "Poppins Regular", Path: `C:\Users\dana\AppData\Local\Microsoft\Windows\Fonts\Poppins-Regular.ttf`, Same: true}}}, nil)
	var devs []deviceRow
	admin.do("GET", "/api/devices", nil, &devs)
	if len(devs) != 1 || len(devs[0].Existing) != 1 || !devs[0].Existing[0].Same || devs[0].Existing[0].FullName != "Poppins Regular" {
		t.Fatalf("devices: %+v", devs)
	}
	// the next report replaces it
	devicePost(t, ts.URL, dev, "/api/agent/report", protocol.Report{AgentVersion: "1.0.0", Installed: []string{fonts[0].SHA256}}, nil)
	admin.do("GET", "/api/devices", nil, &devs)
	if len(devs[0].Existing) != 0 || devs[0].Installed != 1 {
		t.Fatalf("after: %+v", devs[0])
	}
}
