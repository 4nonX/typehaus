// Command fontscan decodes every file in the given directories with the
// typehaus importer and reports what it cannot read. CI runs it with
// -strict against the system font folders of Windows, macOS and Linux:
// every font file there must import.
//
//	go run ./tools/fontscan [-strict] [-gha] DIR...
package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

type stat struct {
	ok, companion, notFont, bad int
	kinds                       map[string]int
	examples                    []string
}

func main() {
	strict := flag.Bool("strict", false, "exit 1 if any font file is rejected")
	gha := flag.Bool("gha", false, "emit GitHub Actions annotations")
	flag.Parse()
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
				s = &stat{kinds: map[string]int{}}
				stats[ext] = s
			}
			files, err := fontmeta.Decode(p, b)
			switch {
			case err == nil:
				s.ok++
				for _, f := range files {
					k := string(f.Kind)
					if f.Source != "" {
						k += "←" + f.Source
					}
					s.kinds[k]++
				}
			case fontmeta.CompanionExts[ext]:
				s.companion++
			case errors.Is(err, fontmeta.ErrNotFont):
				s.notFont++
			default:
				s.bad++
				if len(s.examples) < 6 {
					s.examples = append(s.examples, fmt.Sprintf("%s [%s] %v", filepath.Base(p), hex.EncodeToString(b[:min(8, len(b))]), err))
				}
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
		parts := []string{}
		if s.ok > 0 {
			var ks []string
			for k, n := range s.kinds {
				ks = append(ks, fmt.Sprintf("%s:%d", k, n))
			}
			sort.Strings(ks)
			parts = append(parts, fmt.Sprintf("%d imported (%s)", s.ok, strings.Join(ks, " ")))
		}
		if s.companion > 0 {
			parts = append(parts, fmt.Sprintf("%d metrics files", s.companion))
		}
		if s.notFont > 0 {
			parts = append(parts, fmt.Sprintf("%d not fonts", s.notFont))
		}
		if s.bad > 0 {
			parts = append(parts, fmt.Sprintf("%d REJECTED – e.g. %s", s.bad, strings.Join(s.examples, " | ")))
			failed = true
		}
		line := e + ": " + strings.Join(parts, ", ")
		fmt.Println(line)
		if *gha {
			level := "notice"
			if s.bad > 0 {
				level = "error"
			}
			fmt.Printf("::%s title=fontscan %s::%s\n", level, e, line)
		}
	}
	if *strict && failed {
		os.Exit(1)
	}
}
