package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Agent programs (typehaus-agent…) and the installers: typehaus-setup-windows.exe,
// typehaus-macos.dmg, typehaus-linux-<arch>.deb / .rpm
var downloadName = regexp.MustCompile(`^(typehaus-agent[a-z0-9_.-]*|typehaus-setup-windows\.exe|typehaus-macos\.dmg|typehaus-linux-(amd64|arm64)\.(deb|rpm))$`)

var installerName = regexp.MustCompile(`^typehaus-(setup-windows\.exe|macos\.dmg|linux-(amd64|arm64)\.(deb|rpm))$`)

type download struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
	Headless  bool   `json:"headless"`
	App       bool   `json:"app"`       // macOS .app bundle (zip)
	Installer bool   `json:"installer"` // setup.exe, .dmg, .deb, .rpm
	Format    string `json:"format"`    // exe, dmg, deb, rpm, zip, "" (program)
}

// handleListDownloads lists agent binaries named
// typehaus-agent-<os>-<arch>[.exe], typehaus-agentw-windows-<arch>.exe and
// typehaus-agent-darwin-universal.app.zip.
func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	out := []download{}
	ents, err := os.ReadDir(s.cfg.AgentsDir)
	if err == nil {
		for _, e := range ents {
			if e.IsDir() || !downloadName.MatchString(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if m := installerName.FindStringSubmatch(e.Name()); m != nil {
				d := download{Name: e.Name(), Size: info.Size(), URL: "/downloads/" + e.Name(), Installer: true}
				switch {
				case strings.HasPrefix(m[1], "setup-windows"):
					d.OS, d.Arch, d.Format = "windows", "universal", "exe"
				case strings.HasPrefix(m[1], "macos"):
					d.OS, d.Arch, d.Format = "darwin", "universal", "dmg"
				default:
					d.OS, d.Arch, d.Format = "linux", m[2], m[3]
				}
				out = append(out, d)
				continue
			}
			base := strings.TrimSuffix(e.Name(), ".exe")
			base = strings.TrimSuffix(base, ".zip")
			isApp := strings.HasSuffix(base, ".app")
			base = strings.TrimSuffix(base, ".app")
			parts := strings.Split(base, "-")
			if len(parts) < 4 {
				continue
			}
			format := ""
			if isApp {
				format = "zip"
			}
			out = append(out, download{
				Name: e.Name(), OS: parts[len(parts)-2], Arch: parts[len(parts)-1], Size: info.Size(),
				URL: "/downloads/" + e.Name(), Headless: strings.HasPrefix(e.Name(), "typehaus-agentw"), App: isApp, Format: format,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !downloadName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.AgentsDir, name)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, p)
}
