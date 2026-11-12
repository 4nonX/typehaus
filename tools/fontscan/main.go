// Command fontscan parses every file in the given directories with the
// typehaus font reader and reports what it cannot read. Used in CI against
// the real system font folders of Windows, macOS and Linux.
//
//	go run ./tools/fontscan [-strict] DIR...
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

func main() {
	strict := flag.Bool("strict", false, "exit 1 if any font-like file is rejected")
	gha := flag.Bool("gha", false, "emit GitHub Actions annotations")
	flag.Parse()
	type stat struct {
		ok, bad  int
		examples []string
	}
	stats := map[string]*stat{}
	for _, root := range flag.Args() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext == "" {
				ext = "(none)"
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			s := stats[ext]
			if s == nil {
				s = &stat{}
				stats[ext] = s
			}
			if _, err := fontmeta.Parse(b); err != nil {
				s.bad++
				if len(s.examples) < 6 {
					n := min(8, len(b))
					s.examples = append(s.examples, fmt.Sprintf("%s [%s] %v", filepath.Base(p), hex.EncodeToString(b[:n]), err))
				}
			} else {
				s.ok++
			}
			return nil
		})
	}
	var exts []string
	for e := range stats {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	failed := false
	for _, e := range exts {
		s := stats[e]
		line := fmt.Sprintf("%s: %d ok, %d rejected", e, s.ok, s.bad)
		if s.bad > 0 {
			line += " – e.g. " + strings.Join(s.examples, " | ")
			failed = true
		}
		fmt.Println(line)
		if *gha {
			fmt.Printf("::notice title=fontscan %s::%s\n", e, line)
		}
	}
	if *strict && failed {
		os.Exit(1)
	}
}
