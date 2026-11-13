// Command adobeplugins writes the Adobe app panels for a release: the UXP
// packages (typehaus-indesign.ccx, typehaus-photoshop.ccx) and the folder
// of the Illustrator extension, which the release build signs into
// typehaus-illustrator.zxp with Adobe's ZXPSignCmd.
//
//	go run ./tools/adobeplugins -version v1.0.0 -out out/adobe
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/4nonX/typehaus/internal/adobeplugins"
)

func main() {
	version := flag.String("version", "dev", "release version")
	out := flag.String("out", "out/adobe", "output folder")
	flag.Parse()
	must(os.MkdirAll(*out, 0o755))
	for _, id := range []string{"indesign", "photoshop"} {
		b, err := adobeplugins.CCX(id, *version)
		must(err)
		must(os.WriteFile(filepath.Join(*out, "typehaus-"+id+".ccx"), b, 0o644))
	}
	files, err := adobeplugins.Files("illustrator", *version)
	must(err)
	dir := filepath.Join(*out, "illustrator")
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, b, 0o644))
	}
	fmt.Println("wrote", *out)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
