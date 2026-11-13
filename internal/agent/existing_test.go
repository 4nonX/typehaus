package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/4nonX/typehaus/internal/protocol"
)

// manifestServer serves a fixed font list and records the reports.
type manifestServer struct {
	mu      sync.Mutex
	files   map[string][]byte // sha → bytes
	fonts   []protocol.ManifestFont
	policy  string
	reports []protocol.Report
}

func (m *manifestServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/manifest", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		etag := `"` + m.policy + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		json.NewEncoder(w).Encode(protocol.Manifest{ProtocolVersion: protocol.Version, User: "dana", PollSeconds: 60,
			Fonts: m.fonts, ExistingFonts: m.policy})
	})
	mux.HandleFunc("GET /api/agent/fonts/{sha}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		b, ok := m.files[r.PathValue("sha")]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("POST /api/agent/report", func(w http.ResponseWriter, r *http.Request) {
		var rep protocol.Report
		json.NewDecoder(r.Body).Decode(&rep)
		m.mu.Lock()
		m.reports = append(m.reports, rep)
		m.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

func (m *manifestServer) last() protocol.Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reports[len(m.reports)-1]
}

func testdataFont(t *testing.T, name string) ([]byte, string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

// A font already installed outside typehaus is never installed a second
// time: the identical file is left as it is, another version under the same
// name is reported. Once the other copy is gone, typehaus installs its own.
func TestExistingFontsNotDuplicated(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux per-user font folder")
	}
	home := t.TempDir()
	t.Setenv("TYPEHAUS_AGENT_HOME", filepath.Join(home, "cfg"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("PATH", "") // no fc-cache needed

	poppins, popSHA := testdataFont(t, "Poppins-Regular.ttf")
	hanken, hanSHA := testdataFont(t, "HankenGrotesk-wght.ttf")
	ms := &manifestServer{files: map[string][]byte{popSHA: poppins, hanSHA: hanken}, policy: "skip",
		fonts: []protocol.ManifestFont{
			{ID: 1, SHA256: popSHA, Filename: "Poppins-Regular.ttf", Family: "Poppins", Style: "Regular", FullName: "Poppins Regular", PostScript: "Poppins-Regular", Format: "ttf", Size: int64(len(poppins))},
			{ID: 2, SHA256: hanSHA, Filename: "HankenGrotesk-wght.ttf", Family: "Hanken Grotesk", Style: "Thin", FullName: "Hanken Grotesk Thin", PostScript: scanFontFile(filepath.Join("..", "..", "testdata", "fonts", "HankenGrotesk-wght.ttf"))[0].PostScript, Format: "ttf", Size: int64(len(hanken))},
		}}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()
	a, err := New(&Config{Server: srv.URL, Token: "dev-token", DeviceID: 1, User: "dana"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// the person installed Poppins themselves
	userFonts := filepath.Join(home, "data", "fonts")
	os.MkdirAll(userFonts, 0o755)
	t.Setenv("TYPEHAUS_INVENTORY_DIRS", userFonts) // not this machine's own fonts
	mine := filepath.Join(userFonts, "Poppins-Regular.ttf")
	if err := os.WriteFile(mine, poppins, 0o644); err != nil {
		t.Fatal(err)
	}
	sync := func(force bool) *State {
		t.Helper()
		forgetSystemFonts()
		if _, err := a.Sync(ctx, force); err != nil {
			t.Fatal(err)
		}
		st, err := LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := sync(false)
	if _, ok := st.Fonts[popSHA]; ok {
		t.Fatal("Poppins installed a second time")
	}
	if _, ok := st.Fonts[hanSHA]; !ok {
		t.Fatal("Hanken Grotesk not installed")
	}
	rep := ms.last()
	if len(rep.Existing) != 1 || rep.Existing[0].SHA256 != popSHA || !rep.Existing[0].Same || rep.Existing[0].Path != mine {
		t.Fatalf("report: %+v", rep.Existing)
	}
	if b, _ := os.ReadFile(mine); string(b) != string(poppins) {
		t.Fatal("the person's own copy was changed")
	}

	// another version under the same name: still not installed, reported as different
	other := append(append([]byte{}, poppins...), 0, 0, 0, 0)
	os.WriteFile(mine, other, 0o644)
	st = sync(true)
	if _, ok := st.Fonts[popSHA]; ok {
		t.Fatal("Poppins installed next to another version")
	}
	if rep := ms.last(); len(rep.Existing) != 1 || rep.Existing[0].Same {
		t.Fatalf("different version: %+v", rep.Existing)
	}

	// the other copy is gone: the next sync installs the library's, without
	// waiting for a changed font list
	os.Remove(mine)
	st = sync(false)
	if _, ok := st.Fonts[popSHA]; !ok || len(st.Existing) != 0 {
		t.Fatalf("not installed after the other copy went: %+v", st.Existing)
	}
	if rep := ms.last(); len(rep.Existing) != 0 || len(rep.Installed) != 2 {
		t.Fatalf("report after: %+v", rep)
	}
	// a copy appearing later doesn't make typehaus remove its own
	os.WriteFile(mine, poppins, 0o644)
	if st = sync(true); st.Fonts[popSHA] == nil {
		t.Fatal("typehaus removed its own copy")
	}

	// policy "install": installs next to an existing copy
	os.Remove(mine)
	ms.mu.Lock()
	ms.policy = "install"
	ms.mu.Unlock()
	os.WriteFile(filepath.Join(userFonts, "Hanken-copy.ttf"), hanken, 0o644)
	a.RemoveAll()
	st = sync(true)
	if st.Fonts[hanSHA] == nil || len(st.Existing) != 0 {
		t.Fatalf("policy install: %+v %+v", st.Fonts, st.Existing)
	}
	if !strings.Contains(st.Fonts[hanSHA].Path, "typehaus") {
		t.Fatal("installed outside typehaus' folder")
	}
}
