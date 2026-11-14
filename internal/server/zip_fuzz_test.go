package server

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeZip(t testing.TB, files map[string][]byte) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	zw.Close()
	return b.Bytes()
}

// FuzzUnzip: uploaded and downloaded archives. Whatever the archive claims,
// unzipFonts never panics and never returns more than its limits allow.
//
//	go test ./internal/server -run '^$' -fuzz '^FuzzUnzip$' -fuzztime 5m
func FuzzUnzip(f *testing.F) {
	ttf, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fonts", "Poppins-Regular.ttf"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(makeZip(f, map[string][]byte{"Poppins/Poppins-Regular.ttf": ttf[:4096], "Poppins/OFL.txt": []byte("license")}))
	f.Add(makeZip(f, map[string][]byte{"Suitcase": []byte("x"), "__MACOSX/._Suitcase": []byte("\x00\x05\x16\x07fork"), "__MACOSX/.DS_Store": []byte("junk")}))
	f.Add(makeZip(f, map[string][]byte{"dir/": nil, "a.pfb": []byte("\x80\x01abc"), "a.afm": []byte("StartFontMetrics")}))
	const maxTotal = 1 << 20
	f.Fuzz(func(t *testing.T, b []byte) {
		files, _ := unzipFonts(bytes.NewReader(b), int64(len(b)), maxTotal)
		var total int
		for _, fl := range files {
			if len(fl.data) == 0 || len(fl.data) > maxFontFile {
				t.Fatalf("%s: %d bytes", fl.name, len(fl.data))
			}
			if strings.Contains(fl.name, "__MACOSX/") {
				t.Fatalf("Mac metadata path returned: %s", fl.name)
			}
			total += len(fl.data)
		}
		if total > maxTotal {
			t.Fatalf("unpacked %d bytes, limit %d", total, maxTotal)
		}
		if len(files) > maxZipEntries {
			t.Fatalf("%d files", len(files))
		}
	})
}

// A zip bomb – many entries of zeros that compress to almost nothing –
// is cut off at the archive's limit instead of filling the memory.
func TestUnzipBomb(t *testing.T) {
	zeros := make([]byte, 4<<20)
	entries := map[string][]byte{}
	for i := 0; i < 40; i++ {
		entries["font"+itoa(int64(i))+".ttf"] = zeros
	}
	z := makeZip(t, entries)
	if len(z) > 1<<20 {
		t.Fatalf("bomb not small: %d", len(z))
	}
	files, errs := unzipFonts(bytes.NewReader(z), int64(len(z)), 32<<20)
	var total int
	for _, f := range files {
		total += len(f.data)
	}
	if total > 32<<20 || len(errs) != 1 || !strings.Contains(errs[0].Message, "unpacks to more than 32 MB") {
		t.Fatalf("total %d, errors %+v", total, errs)
	}

	// too many entries
	many := map[string][]byte{}
	for i := 0; i <= maxZipEntries; i++ {
		many["f"+itoa(int64(i))] = []byte{1}
	}
	z = makeZip(t, many)
	if files, errs := unzipFonts(bytes.NewReader(z), int64(len(z)), 1<<30); len(files) != 0 || len(errs) != 1 {
		t.Fatalf("too many entries: %d files, %+v", len(files), errs)
	}
}

// FuzzAsposeFonts: Aspose's identification result, a base64 Python-style
// dict from an outside service.
func FuzzAsposeFonts(f *testing.F) {
	f.Add("eydzdHlsZSc6ICdyZWd1bGFyJywgJ2ZvbnQnOiBbJ3RhaG9tYScsICd0aW1lcycsICd2ZXJkYW5hJ119")
	f.Add("e30=")
	f.Fuzz(func(t *testing.T, data string) {
		parseAsposeFonts(data)
	})
}
