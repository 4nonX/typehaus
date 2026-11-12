package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var downloadName = regexp.MustCompile(`^typehaus-agent[a-z0-9_.-]*$`)

type download struct {
	Name     string `json:"name"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	Headless bool   `json:"headless"`
	App      bool   `json:"app"` // macOS .app bundle (zip)
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
			base := strings.TrimSuffix(e.Name(), ".exe")
			base = strings.TrimSuffix(base, ".zip")
			isApp := strings.HasSuffix(base, ".app")
			base = strings.TrimSuffix(base, ".app")
			parts := strings.Split(base, "-")
			if len(parts) < 4 {
				continue
			}
			out = append(out, download{
				Name: e.Name(), OS: parts[len(parts)-2], Arch: parts[len(parts)-1], Size: info.Size(),
				URL: "/downloads/" + e.Name(), Headless: strings.HasPrefix(e.Name(), "typehaus-agentw"), App: isApp,
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
