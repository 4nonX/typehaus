package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Instance describes the running interactive agent (tray app).
type Instance struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
	URL     string `json:"url"` // agent window incl. access token
}

func instancePath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "instance.json"), err
}

// PublishInstance records this process as the running tray app.
func PublishInstance(windowURL string) {
	if p, err := instancePath(); err == nil {
		writeJSONFile(p, Instance{PID: os.Getpid(), Version: Version, URL: windowURL}, 0o600)
	}
}

// ClearInstance removes the record on exit (if it is ours).
func ClearInstance() {
	if r := runningRecord(); r != nil && r.PID != os.Getpid() {
		return
	}
	if p, err := instancePath(); err == nil {
		os.Remove(p)
	}
}

func runningRecord() *Instance {
	p, err := instancePath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var in Instance
	if json.Unmarshal(b, &in) != nil {
		return nil
	}
	return &in
}

// RunningInstance returns the other running tray app, if any.
func RunningInstance() *Instance {
	in := runningRecord()
	if in == nil || in.PID == os.Getpid() || !processAlive(in.PID) || in.URL == "" {
		return nil
	}
	return in
}

// Call invokes an action on the running instance's window API
// (e.g. "sync", "quit").
func (in *Instance) Call(action string) error {
	u, err := url.Parse(in.URL)
	if err != nil {
		return err
	}
	tok := u.Query().Get("t")
	req, _ := http.NewRequest("POST", u.Scheme+"://"+u.Host+"/api/"+action, nil)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("X-Typehaus-Token", tok)
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(resp.Status)
	}
	return nil
}

// Replace asks the running instance to quit (gracefully, so its tray icon
// disappears) and waits for it; it is killed if it does not exit in time.
func (in *Instance) Replace() {
	in.Call("quit")
	for i := 0; i < 50 && processAlive(in.PID); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(in.PID) {
		if p, err := os.FindProcess(in.PID); err == nil {
			p.Kill()
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// RefreshAutostart re-registers login autostart for the binary that is
// running now, when autostart is on but points at another copy (e.g. after
// downloading a newer version). Without this, login would keep starting the
// old version.
func RefreshAutostart() {
	if !AutostartEnabled() {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	self, _ = filepath.EvalSymlinks(self)
	if dir, err := binDir(); err == nil && strings.HasPrefix(self, dir+string(filepath.Separator)) {
		return // we are the installed copy
	}
	EnableAutostart("tray", false)
}

// ---- small preferences that survive disconnecting -------------------------

type prefs struct {
	LastServer string `json:"last_server,omitempty"`
}

func prefsPath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "prefs.json"), err
}

func loadPrefs() prefs {
	var p prefs
	if path, err := prefsPath(); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, &p)
		}
	}
	return p
}

// LastServer is the server this computer was last connected to.
func LastServer() string { return loadPrefs().LastServer }

func rememberServer(s string) {
	p := loadPrefs()
	p.LastServer = s
	if path, err := prefsPath(); err == nil {
		writeJSONFile(path, p, 0o600)
	}
}
