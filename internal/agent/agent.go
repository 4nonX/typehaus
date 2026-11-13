// Package agent implements the typehaus desktop sync agent. It keeps the
// fonts a user activated on the server installed for that user on this
// machine – without administrator rights – and removes them again when they
// are deactivated or access is revoked.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/4nonX/typehaus/internal/protocol"
)

// Version is set at build time.
var Version = "dev"

// ErrRevoked means the server no longer accepts this device's token.
var ErrRevoked = errors.New("this computer was disconnected by the server")

// ErrSuspended means the user's account is disabled. The enrollment stays
// valid, but no fonts may be used until the account is enabled again.
var ErrSuspended = errors.New("your typehaus account is disabled")

// Config is persisted after enrollment.
type Config struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	DeviceID int64  `json:"device_id"`
	User     string `json:"user"`
	CAFile   string `json:"ca_file,omitempty"`
	// KeepOnRevoke leaves fonts installed if the device is revoked.
	KeepOnRevoke bool `json:"keep_on_revoke,omitempty"`
}

// Installed records a font file this agent placed on the system.
type Installed struct {
	SHA256   string `json:"sha256"`
	Path     string `json:"path"`
	FullName string `json:"full_name"`
	Family   string `json:"family"`
	Style    string `json:"style"`
	// RegName is the per-user registry value name (Windows only).
	RegName string `json:"reg_name,omitempty"`
	// Kind: "" (TrueType/OpenType), type1, winfnt, pcf, bdf.
	Kind string `json:"kind,omitempty"`
	// Extras are companion files (Type 1 metrics) installed with the font.
	Extras []string `json:"extras,omitempty"`
	// PendingRemoval is set when a deactivated font could not be removed yet
	// (e.g. file locked by a running application).
	PendingRemoval bool `json:"pending_removal,omitempty"`
	// LeaseDays > 0: remove the font after this many days without contact to
	// the server (the license's offline lease).
	LeaseDays int `json:"lease_days,omitempty"`
}

// State is the agent's local bookkeeping.
type State struct {
	ETag          string                `json:"etag"`
	ServerVersion string                `json:"server_version,omitempty"`
	LastSync      time.Time             `json:"last_sync"`
	LastErr       string                `json:"last_error,omitempty"`
	Fonts         map[string]*Installed `json:"fonts"`
	// Paused lists fonts removed because their offline lease ran out; they
	// are installed again by the next successful sync.
	Paused []string `json:"paused,omitempty"`
	// Inventory: the server asks for the fonts installed outside typehaus;
	// InventoryAt is when they were last sent.
	Inventory   bool      `json:"inventory,omitempty"`
	InventoryAt time.Time `json:"inventory_at,omitempty"`
	// Existing: library fonts not installed because the computer already
	// has a font with the same PostScript name outside typehaus, by sha256.
	Existing map[string]*protocol.Existing `json:"existing,omitempty"`
}

// ConfigDir returns the per-user directory for config and state (or the
// protected machine directory in machine-wide mode).
func ConfigDir() (string, error) {
	if machineMode {
		return machineDir()
	}
	if d := os.Getenv("TYPEHAUS_AGENT_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "typehaus"), nil
}

func configPath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "agent.json"), err
}

func statePath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "state.json"), err
}

// LoadConfig reads the enrollment config.
func LoadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("this device is not enrolled – run 'typehaus-agent enroll' first")
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// SaveConfig writes the enrollment config (mode 0600: contains the token).
func SaveConfig(c *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	return writeJSONFile(p, c, 0o600)
}

// RemoveConfig deletes config and state.
func RemoveConfig() error {
	for _, f := range []func() (string, error){configPath, statePath} {
		p, err := f()
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// LoadState reads local state; a missing file yields an empty state.
func LoadState() (*State, error) {
	st := &State{Fonts: map[string]*Installed{}}
	p, err := statePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if st.Fonts == nil {
		st.Fonts = map[string]*Installed{}
	}
	return st, nil
}

func saveState(st *State) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	return writeJSONFile(p, st, 0o600)
}

func writeJSONFile(p string, v any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---- HTTP -------------------------------------------------------------------

// NewHTTPClient builds a client trusting system roots plus an optional CA file.
func NewHTTPClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file contains no PEM certificates")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Minute}, nil
}

func userAgent() string {
	return fmt.Sprintf("typehaus-agent/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
}

// Enroll exchanges a one-time code for a device token and saves the config.
func Enroll(ctx context.Context, server, code, name, caFile string) (*Config, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	hc, err := NewHTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	platform := runtime.GOOS
	if machineMode {
		platform += "-machine" // shown as "all users" in the device list
	}
	body, _ := json.Marshal(protocol.EnrollRequest{
		Code: code, DeviceName: name, Platform: platform, Hostname: host, AgentVersion: Version,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", server+"/api/agent/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("enrollment failed: %s", apiError(resp))
	}
	var er protocol.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	abs := caFile
	if abs != "" {
		abs, _ = filepath.Abs(caFile)
	}
	c := &Config{Server: server, Token: er.Token, DeviceID: er.DeviceID, User: er.User, CAFile: abs}
	rememberServer(server)
	return c, SaveConfig(c)
}

func apiError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Error, resp.StatusCode)
	}
	return resp.Status
}

// ---- sync ---------------------------------------------------------------------

// Agent performs synchronisation.
type Agent struct {
	cfg *Config
	hc  *http.Client
	log *slog.Logger
	sys platform

	// OnApply, if set, is called before fonts are installed or removed with
	// the number of pending changes (used to animate the tray icon).
	OnApply func(changes int)

	// SyncNow wakes Run for an immediate sync (plugins activating fonts).
	SyncNow chan struct{}
}

// New creates an agent for an enrolled device.
func New(cfg *Config, log *slog.Logger) (*Agent, error) {
	hc, err := NewHTTPClient(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	sys, err := newPlatform(log)
	if err != nil {
		return nil, err
	}
	return &Agent{cfg: cfg, hc: hc, log: log, sys: sys, SyncNow: make(chan struct{}, 1)}, nil
}

func (a *Agent) do(ctx context.Context, method, path string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.Server+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("User-Agent", userAgent())
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		resp.Body.Close()
		return nil, ErrRevoked
	case http.StatusForbidden:
		resp.Body.Close()
		return nil, ErrSuspended
	}
	return resp, nil
}

// SyncResult summarises one sync pass.
type SyncResult struct {
	Unchanged   bool
	PollSeconds int
	Added       []string
	Removed     []string
	Errors      []protocol.ReportError
}

// Sync performs one reconciliation pass. force ignores the cached ETag.
func (a *Agent) Sync(ctx context.Context, force bool) (*SyncResult, error) {
	st, err := LoadState()
	if err != nil {
		return nil, err
	}
	res := &SyncResult{PollSeconds: 60}

	hdr := map[string]string{}
	if !force && st.ETag != "" && !a.needsRepair(st) {
		hdr["If-None-Match"] = st.ETag
	}
	resp, err := a.do(ctx, "GET", "/api/agent/manifest", nil, hdr)
	if err != nil {
		if !errors.Is(err, ErrRevoked) && !errors.Is(err, ErrSuspended) {
			a.expireLeases(st, time.Now())
		}
		st.LastErr = err.Error()
		saveState(st)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		st.LastSync, st.LastErr, st.Paused = time.Now(), "", nil
		saveState(st)
		res.Unchanged = true
		res.PollSeconds = 0
		return res, nil
	}
	if resp.StatusCode != 200 {
		err := fmt.Errorf("manifest: %s", apiError(resp))
		a.expireLeases(st, time.Now())
		st.LastErr = err.Error()
		saveState(st)
		return nil, err
	}
	var m protocol.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.ProtocolVersion > protocol.Version {
		a.log.Warn("server speaks a newer protocol; please update the agent", "server", m.ProtocolVersion, "agent", protocol.Version)
	}
	res.PollSeconds = m.PollSeconds
	st.ServerVersion = m.ServerVersion
	st.Inventory = m.Inventory

	want := map[string]protocol.ManifestFont{}
	machine := map[string]bool{}
	if !machineMode {
		machine = MachineInstalled() // fonts already installed for all users on this PC
	}
	for _, f := range m.Fonts {
		if machine[f.SHA256] {
			continue // avoid the same font twice (per-user and machine-wide)
		}
		want[f.SHA256] = f
	}
	// never a second copy of a font the computer already has
	st.Existing = nil
	if m.ExistingFonts != "install" {
		st.Existing = a.existingFonts(ctx, st, want)
		for sha := range st.Existing {
			delete(want, sha)
		}
	}
	changed := false

	if a.OnApply != nil {
		pending := 0
		for sha, inst := range st.Fonts {
			if _, ok := want[sha]; !ok || inst.PendingRemoval {
				pending++
			}
		}
		for sha := range want {
			if _, ok := st.Fonts[sha]; !ok {
				pending++
			}
		}
		if pending > 0 {
			a.OnApply(pending)
		}
	}

	// remove fonts no longer wanted (and retry pending removals)
	for sha, inst := range st.Fonts {
		if _, ok := want[sha]; ok && !inst.PendingRemoval {
			continue
		}
		if _, ok := want[sha]; ok && inst.PendingRemoval {
			// Re-activated before the removal finished. The font was already
			// unregistered (Windows: resource and registry value removed), only
			// the locked file is left – register it again, or reinstall below
			// if the file is gone or damaged.
			if fileHasHash(inst.Path, sha) && a.sys.install(inst) == nil {
				inst.PendingRemoval = false
				changed = true
			} else {
				delete(st.Fonts, sha)
			}
			continue
		}
		if err := a.sys.uninstall(inst); err != nil {
			inst.PendingRemoval = true
			a.log.Warn("could not remove font yet, will retry", "font", inst.FullName, "err", err)
			continue
		}
		delete(st.Fonts, sha)
		res.Removed = append(res.Removed, inst.FullName)
		changed = true
	}

	// install missing or damaged fonts
	for sha, mf := range want {
		if inst, ok := st.Fonts[sha]; ok && !inst.PendingRemoval && fileHasHash(inst.Path, sha) {
			inst.LeaseDays = mf.LeaseDays
			continue
		} else if ok {
			a.sys.uninstall(inst) // clean up damaged/missing entry before reinstalling
			delete(st.Fonts, sha)
		}
		inst, err := a.installOne(ctx, mf)
		if errors.Is(err, ErrRevoked) {
			return nil, err
		}
		if err != nil {
			a.log.Error("install failed", "font", mf.FullName, "err", err)
			res.Errors = append(res.Errors, protocol.ReportError{SHA256: sha, Message: err.Error()})
			continue
		}
		st.Fonts[sha] = inst
		res.Added = append(res.Added, mf.FullName)
		changed = true
	}

	if changed {
		a.sys.refresh()
	}
	if len(res.Errors) == 0 {
		st.ETag = resp.Header.Get("ETag")
	} else {
		st.ETag = "" // retry failed installs on next pass
	}
	st.LastSync, st.LastErr, st.Paused = time.Now(), "", nil
	if err := saveState(st); err != nil {
		return nil, err
	}
	if machineMode {
		publishMachineFonts(st)
	}
	if err := a.report(ctx, st, res.Errors); err != nil {
		a.log.Warn("report failed", "err", err)
	}
	return res, nil
}

// needsRepair reports whether any recorded font is missing on disk or a
// removal is still pending – in that case the manifest must be re-applied.
func (a *Agent) needsRepair(st *State) bool {
	for _, e := range st.Existing {
		if _, err := os.Stat(e.Path); err != nil {
			return true // the other copy is gone: install the library's
		}
	}
	for _, f := range st.Fonts {
		if f.PendingRemoval {
			return true
		}
		if _, err := os.Stat(f.Path); err != nil {
			return true
		}
	}
	return false
}

// existingFonts finds the fonts about to be installed whose PostScript name
// is already installed on this computer outside typehaus – the system's
// fonts, ones the person installed themselves, Adobe Fonts. typehaus never
// puts a second copy next to them: two fonts with one name make apps pick
// either, or list it twice. Fonts typehaus already installed are not
// checked again.
func (a *Agent) existingFonts(ctx context.Context, st *State, want map[string]protocol.ManifestFont) map[string]*protocol.Existing {
	var todo []protocol.ManifestFont
	for sha, mf := range want {
		if _, ok := st.Fonts[sha]; !ok && mf.PostScript != "" {
			todo = append(todo, mf)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	own := map[string]bool{}
	for _, f := range st.Fonts {
		own[filepath.Clean(f.Path)] = true
	}
	for _, p := range MachineFontPaths() {
		own[filepath.Clean(p)] = true
	}
	faces := systemFontFaces(ctx)
	out := map[string]*protocol.Existing{}
	for _, mf := range todo {
		var hit *protocol.Existing
		for _, f := range faces[strings.ToLower(mf.PostScript)] {
			if own[f.Path] {
				continue
			}
			if f.SHA256 == mf.SHA256 {
				hit = &protocol.Existing{SHA256: mf.SHA256, FullName: mf.FullName, Path: f.Path, Same: true}
				break
			}
			if hit == nil {
				hit = &protocol.Existing{SHA256: mf.SHA256, FullName: mf.FullName, Path: f.Path}
			}
		}
		if hit != nil {
			out[mf.SHA256] = hit
			a.log.Info("font already installed outside typehaus – not installing a second copy",
				"font", mf.FullName, "path", hit.Path, "identical", hit.Same)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]`)

func (a *Agent) installOne(ctx context.Context, mf protocol.ManifestFont) (*Installed, error) {
	dir, err := a.sys.fontDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := unsafeName.ReplaceAllString(filepath.Base(mf.Filename), "_")
	if name == "" || name == "." {
		name = "font." + mf.Format
	}
	// companions share the font's base name (Windows pairs x.pfm|x.pfb,
	// FreeType finds x.afm next to x.pfb)
	stem := a.sys.filePrefix() + mf.SHA256[:8] + "-" + strings.TrimSuffix(name, filepath.Ext(name))
	final := filepath.Join(dir, stem+filepath.Ext(name))

	inst := &Installed{SHA256: mf.SHA256, Path: final, FullName: mf.FullName, Family: mf.Family, Style: mf.Style, Kind: mf.Kind, LeaseDays: mf.LeaseDays}
	cleanup := func() {
		os.Remove(final)
		for _, e := range inst.Extras {
			os.Remove(e)
		}
	}
	if err := a.download(ctx, mf.SHA256, dir, final); err != nil {
		return nil, err
	}
	for _, e := range mf.Extras {
		ext := strings.ToLower(e.Ext)
		if !strings.HasPrefix(ext, ".") || unsafeName.MatchString(ext) || len(ext) > 6 {
			continue
		}
		p := filepath.Join(dir, stem+ext)
		if err := a.download(ctx, e.SHA256, dir, p); err != nil {
			cleanup()
			return nil, fmt.Errorf("metrics file %s: %w", ext, err)
		}
		inst.Extras = append(inst.Extras, p)
	}
	if err := a.sys.install(inst); err != nil {
		cleanup()
		return nil, err
	}
	return inst, nil
}

// download fetches a file by hash into dst, verifying its SHA-256.
func (a *Agent) download(ctx context.Context, sha, dir, dst string) error {
	resp, err := a.do(ctx, "GET", "/api/agent/fonts/"+sha, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: %s", apiError(resp))
	}
	tmp, err := os.CreateTemp(dir, ".typehaus-dl-*")
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 128<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != sha {
		err = errors.New("checksum mismatch")
	}
	if err == nil {
		os.Remove(dst)
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// ReloadSession re-registers fonts that Windows only keeps for the current
// session (bitmap .fon and PostScript Type 1 installed per user). Called
// when the agent starts, i.e. at every login.
func (a *Agent) ReloadSession() {
	st, err := LoadState()
	if err != nil {
		return
	}
	n := 0
	for _, f := range st.Fonts {
		if !f.PendingRemoval && a.sys.reload(f) {
			n++
		}
	}
	if n > 0 {
		a.sys.refresh()
	}
}

func (a *Agent) report(ctx context.Context, st *State, errs []protocol.ReportError) error {
	rep := protocol.Report{AgentVersion: Version, Installed: []string{}, Errors: errs}
	for _, e := range st.Existing {
		rep.Existing = append(rep.Existing, *e)
	}
	sort.Slice(rep.Existing, func(i, j int) bool { return rep.Existing[i].SHA256 < rep.Existing[j].SHA256 })
	for sha, f := range st.Fonts {
		if !f.PendingRemoval {
			rep.Installed = append(rep.Installed, sha)
		}
	}
	b, _ := json.Marshal(rep)
	resp, err := a.do(ctx, "POST", "/api/agent/report", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(apiError(resp))
	}
	return nil
}

// RemoveAll uninstalls every font this agent installed.
func (a *Agent) RemoveAll() (removed int, failed int, err error) {
	st, err := LoadState()
	if err != nil {
		return 0, 0, err
	}
	for sha, inst := range st.Fonts {
		if err := a.sys.uninstall(inst); err != nil {
			a.log.Warn("could not remove font", "font", inst.FullName, "err", err)
			inst.PendingRemoval = true
			failed++
			continue
		}
		delete(st.Fonts, sha)
		removed++
	}
	st.ETag = ""
	a.sys.refresh()
	if machineMode {
		publishMachineFonts(st)
	}
	return removed, failed, saveState(st)
}

// expireLeases removes fonts whose offline lease ran out: the server has not
// been reached for longer than their license allows. A clock set back before
// the last contact counts as expired too. The fonts return with the next
// successful sync (the cleared ETag forces a full manifest).
func (a *Agent) expireLeases(st *State, now time.Time) (removed []string) {
	if st.LastSync.IsZero() {
		return nil
	}
	away := now.Sub(st.LastSync)
	changed := false
	for sha, inst := range st.Fonts {
		if inst.LeaseDays <= 0 {
			continue
		}
		if away <= time.Duration(inst.LeaseDays)*24*time.Hour && away > -24*time.Hour {
			continue
		}
		if err := a.sys.uninstall(inst); err != nil {
			if !inst.PendingRemoval {
				a.log.Warn("offline lease ended, but the font could not be removed yet", "font", inst.FullName, "err", err)
			}
			inst.PendingRemoval = true
			continue
		}
		delete(st.Fonts, sha)
		removed = append(removed, inst.FullName)
		changed = true
	}
	if !changed {
		return nil
	}
	a.log.Warn("offline lease ended – fonts removed until the server is reachable again", "fonts", len(removed))
	st.Paused = append(st.Paused, removed...)
	st.ETag = ""
	a.sys.refresh()
	if machineMode {
		publishMachineFonts(st)
	}
	return removed
}

// Run syncs in a loop until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.ReloadSession()
	interval := 60 * time.Second
	backoff := 0
	for {
		res, err := a.Sync(ctx, false)
		switch {
		case errors.Is(err, ErrRevoked):
			a.log.Error("device was revoked on the server")
			if !a.cfg.KeepOnRevoke {
				n, _, _ := a.RemoveAll()
				a.log.Info("removed fonts after revocation", "count", n)
			}
			return err
		case errors.Is(err, ErrSuspended):
			if !a.cfg.KeepOnRevoke {
				if n, _, _ := a.RemoveAll(); n > 0 {
					a.log.Warn("account disabled – removed fonts", "count", n)
				}
			}
			backoff = 0
			interval = 5 * time.Minute
		case err != nil:
			backoff = min(backoff+1, 6)
			a.log.Warn("sync failed – fonts stay active, retrying", "err", err)
		default:
			backoff = 0
			if res.PollSeconds >= 10 {
				interval = time.Duration(res.PollSeconds) * time.Second
			}
			if len(res.Added)+len(res.Removed)+len(res.Errors) > 0 {
				a.log.Info("sync complete", "added", len(res.Added), "removed", len(res.Removed), "errors", len(res.Errors))
			}
			a.MaybeInventory(ctx)
		}
		wait := interval
		if backoff > 0 {
			wait = min(time.Duration(1<<backoff)*15*time.Second, 15*time.Minute)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		case <-a.SyncNow:
		}
	}
}

func fileHasHash(p, sha string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == sha
}

// platform abstracts per-OS font activation.
type platform interface {
	fontDir() (string, error)
	filePrefix() string
	install(*Installed) error
	uninstall(*Installed) error
	// reload re-registers a font for the current session if the OS doesn't
	// persist it; reports whether it did anything.
	reload(*Installed) bool
	refresh()
}

// removeExtras deletes a font's companion files.
func removeExtras(f *Installed) {
	for _, e := range f.Extras {
		if err := os.Remove(e); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
	}
}

// extraWithExt returns the companion file with the given extension.
func extraWithExt(f *Installed, ext string) string {
	for _, e := range f.Extras {
		if strings.EqualFold(filepath.Ext(e), ext) {
			return e
		}
	}
	return ""
}
