// Package adobeplugins holds the typehaus panels for Adobe apps and builds
// their packages: UXP plugins (.ccx) for InDesign and Photoshop, and a CEP
// extension for Illustrator (signed into a .zxp by the release build).
//
// The panels ask the typehaus app on the same computer about the open
// document's fonts – see internal/agent/pluginapi.go – and live in src/:
// shared/ (panel logic and style), uxp/ (start-up and the file bridge for
// UXP hosts), and one folder per app.
package adobeplugins

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/brand"
)

//go:embed all:src
var src embed.FS

// Plugin is one app's package.
type Plugin struct {
	ID     string `json:"id"`     // indesign, photoshop, illustrator
	App    string `json:"app"`    // InDesign, Photoshop, Illustrator
	File   string `json:"file"`   // download name
	Format string `json:"format"` // ccx, zxp
}

// Plugins lists the packages.
var Plugins = []Plugin{
	{"indesign", "InDesign", "typehaus-indesign.ccx", "ccx"},
	{"photoshop", "Photoshop", "typehaus-photoshop.ccx", "ccx"},
	{"illustrator", "Illustrator", "typehaus-illustrator.zxp", "zxp"},
}

// ByFile finds a plugin by its download name.
func ByFile(name string) *Plugin {
	for i := range Plugins {
		if Plugins[i].File == name {
			return &Plugins[i]
		}
	}
	return nil
}

var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

// manifestVersion turns a build version ("v1.2.0", "1.2.0-3-gabc", "dev")
// into the x.y.z Adobe manifests require.
func manifestVersion(v string) string {
	if m := semver.FindString(v); m != "" {
		return m
	}
	return "1.0.0"
}

func icons(prefix string) map[string][]byte {
	out := map[string][]byte{}
	for _, ic := range []struct {
		name string
		px   int
	}{{"panel", 23}, {"plugin", 48}} {
		out[prefix+ic.name+".png"] = brand.PNG(brand.Mark(ic.px, brand.OnDark, brand.StateOK, 0))
		out[prefix+ic.name+"@2x.png"] = brand.PNG(brand.Mark(ic.px*2, brand.OnDark, brand.StateOK, 0))
	}
	return out
}

func read(p string) []byte {
	b, err := src.ReadFile(path.Join("src", p))
	if err != nil {
		panic("adobeplugins: missing " + p) // embedded: can't happen in a build
	}
	return b
}

// Files returns the files of a plugin, as they go into its package.
func Files(id, version string) (map[string][]byte, error) {
	v := manifestVersion(version)
	files := map[string][]byte{
		"typehaus.js": read("shared/typehaus.js"),
		"panel.css":   read("shared/panel.css"),
	}
	switch id {
	case "indesign", "photoshop":
		for _, f := range []string{"main.js", "bridge.js", "index.html"} {
			files[f] = read("uxp/" + f)
		}
		files["host.js"] = read(id + "/host.js")
		files["manifest.json"] = bytes.Replace(read(id+"/manifest.json"), []byte(`"version": "1.0.0"`), []byte(`"version": "`+v+`"`), 1)
	case "illustrator":
		for _, f := range []string{"index.html", "host.js", "host.jsx"} {
			files[f] = read("illustrator/" + f)
		}
		m := read("illustrator/CSXS/manifest.xml")
		m = bytes.ReplaceAll(m, []byte(`Version="1.0.0"`), []byte(`Version="`+v+`"`))
		files["CSXS/manifest.xml"] = m
	default:
		return nil, errors.New("unknown plugin " + id)
	}
	for k, b := range icons("icons/") {
		files[k] = b
	}
	return files, nil
}

// Zip packs files (a .ccx is a zip with manifest.json at the top).
func Zip(files map[string][]byte) []byte {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // reproducible packages
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			panic(err)
		}
		w.Write(files[n])
	}
	zw.Close()
	return buf.Bytes()
}

// CCX builds the UXP package of InDesign or Photoshop.
func CCX(id, version string) ([]byte, error) {
	if id != "indesign" && id != "photoshop" {
		return nil, errors.New(id + " isn't a UXP plugin")
	}
	files, err := Files(id, version)
	if err != nil {
		return nil, err
	}
	return Zip(files), nil
}

// Sources lists the embedded source files (for tests).
func Sources() []string {
	var out []string
	fs.WalkDir(src, "src", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, "src/"))
		}
		return nil
	})
	return out
}
