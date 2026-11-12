//go:build windows

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Machine-wide mode installs fonts for every user of the computer
// (%WINDIR%\Fonts + HKLM). Some older Windows applications only list fonts
// installed this way. It runs as a Windows service under LocalSystem and is
// set up once by an administrator; the per-user tray app keeps working next
// to it and skips fonts the machine already has.

const (
	ServiceName       = "typehaus"
	machineFontsKey   = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`
	moveDelayUntilReb = 0x4 // MOVEFILE_DELAY_UNTIL_REBOOT
)

var machineMode bool

// SetMachineMode switches the agent to machine-wide operation.
func SetMachineMode() { machineMode = true }

// MachineSupported reports whether this platform has a machine-wide mode.
func MachineSupported() bool { return true }

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// machineDir holds config (with the device token) and state. Only SYSTEM
// and Administrators may access it.
func machineDir() (string, error) {
	d := filepath.Join(programData(), "typehaus", "machine")
	return d, ensureDirACL(d, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
}

// publicDir holds the list of machine-wide fonts, readable by all users so
// the per-user app can skip and load them.
func publicDir() (string, error) {
	d := filepath.Join(programData(), "typehaus", "public")
	return d, ensureDirACL(d, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;BU)")
}

var aclOnce sync.Map

func ensureDirACL(dir, sddl string) error {
	if _, done := aclOnce.Load(dir); done {
		return nil
	}
	if !machineMode {
		return nil // readers (per-user app) never create or modify these
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect %s: %w", dir, err)
	}
	aclOnce.Store(dir, true)
	return nil
}

type machineFont struct {
	SHA256   string `json:"sha256"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Resource string `json:"resource,omitempty"` // AddFontResource string (Type 1: pfm|pfb)
}

func publishMachineFonts(st *State) {
	d, err := publicDir()
	if err != nil {
		return
	}
	list := []machineFont{}
	for sha, f := range st.Fonts {
		if !f.PendingRemoval {
			res, _ := winResource(f)
			list = append(list, machineFont{SHA256: sha, Path: f.Path, Name: f.FullName, Resource: res})
		}
	}
	writeJSONFile(filepath.Join(d, "fonts.json"), list, 0o644)
}

func readMachineFonts() []machineFont {
	b, err := os.ReadFile(filepath.Join(programData(), "typehaus", "public", "fonts.json"))
	if err != nil {
		return nil
	}
	var list []machineFont
	json.Unmarshal(b, &list)
	return list
}

// MachineInstalled returns the fonts installed machine-wide on this PC.
func MachineInstalled() map[string]bool {
	out := map[string]bool{}
	for _, f := range readMachineFonts() {
		out[f.SHA256] = true
	}
	return out
}

var (
	sessionMu     sync.Mutex
	sessionLoaded = map[string]bool{}
)

// LoadMachineFontsIntoSession makes newly installed machine-wide fonts
// usable in this user session right away (the service runs in session 0 and
// cannot reach it; Windows would otherwise load them at the next sign-in).
func LoadMachineFontsIntoSession() {
	if machineMode {
		return
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	current := map[string]bool{}
	changed := false
	for _, f := range readMachineFonts() {
		res := f.Resource
		if res == "" {
			res = f.Path
		}
		current[res] = true
		if sessionLoaded[res] {
			continue
		}
		if addFontResource(res) {
			sessionLoaded[res] = true
			changed = true
		}
	}
	for res := range sessionLoaded {
		if !current[res] {
			removeFontResource(res)
			delete(sessionLoaded, res)
			changed = true
		}
	}
	if changed {
		(&winPlatform{}).refresh()
	}
}

// ---- machine platform: %WINDIR%\Fonts + HKLM -------------------------------

type machinePlatform struct{ log *slog.Logger }

func (p *machinePlatform) fontDir() (string, error) {
	w := os.Getenv("WINDIR")
	if w == "" {
		w = `C:\Windows`
	}
	return filepath.Join(w, "Fonts"), nil
}

func (p *machinePlatform) filePrefix() string { return "typehaus-" }

const machineType1Key = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Type 1 Installer\Type 1 Fonts`

func (p *machinePlatform) install(f *Installed) error {
	res, err := winResource(f)
	if err != nil {
		return err
	}
	if !addFontResource(res) {
		return fmt.Errorf("Windows rejected %s (unsupported or corrupt font)", filepath.Base(f.Path))
	}
	f.RegName = f.FullName + " [typehaus " + f.SHA256[:8] + "]"
	if f.Kind == "type1" {
		// Type 1 registration: "T", metrics file, outline file (names inside %WINDIR%\Fonts)
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, machineType1Key, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("open HKLM Type 1 key: %w", err)
		}
		defer k.Close()
		return k.SetStringsValue(f.RegName, []string{"T", filepath.Base(extraWithExt(f, ".pfm")), filepath.Base(f.Path)})
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, machineFontsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKLM font key: %w", err)
	}
	defer k.Close()
	// files inside %WINDIR%\Fonts are registered by file name
	return k.SetStringValue(f.RegName, filepath.Base(f.Path))
}

func (p *machinePlatform) uninstall(f *Installed) error {
	if res, err := winResource(f); err == nil {
		removeFontResource(res)
	}
	if f.RegName != "" {
		key := machineFontsKey
		if f.Kind == "type1" {
			key = machineType1Key
		}
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.SET_VALUE); err == nil {
			if err := k.DeleteValue(f.RegName); err != nil && !errors.Is(err, registry.ErrNotExist) {
				p.log.Warn("delete registry value", "name", f.RegName, "err", err)
			}
			k.Close()
		}
	}
	for _, path := range append([]string{f.Path}, f.Extras...) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			// In use by an application in some session: it is no longer
			// registered, so delete it at the next restart.
			from, perr := windows.UTF16PtrFromString(path)
			if perr != nil {
				return err
			}
			if merr := windows.MoveFileEx(from, nil, moveDelayUntilReb); merr != nil {
				return err
			}
			p.log.Info("font in use – will be deleted at the next restart", "file", path)
		}
	}
	return nil
}

// reload: machine-wide fonts are loaded by Windows at every logon.
func (p *machinePlatform) reload(*Installed) bool { return false }

func (p *machinePlatform) refresh() { (&winPlatform{}).refresh() }

// ---- service management ------------------------------------------------------

// IsElevated reports whether the process runs with administrator rights.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// InstallService registers and starts the machine-wide service. The agent
// binary is copied to %ProgramFiles%\typehaus so the service doesn't depend
// on a file in someone's Downloads folder.
func InstallService() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dir := filepath.Join(pf, "typehaus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "typehaus-agent.exe")
	m, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(ServiceName); err == nil { // update in place
		s.Control(svc.Stop)
		waitStopped(s)
		s.Delete()
		s.Close()
		time.Sleep(time.Second)
	}
	if !sameFile(self, bin) {
		if err := copyFile(self, bin); err != nil {
			return "", fmt.Errorf("copy agent to %s: %w", dir, err)
		}
	}
	s, err := m.CreateService(ServiceName, bin, mgr.Config{
		DisplayName:      "typehaus (all users)",
		Description:      "Installs the fonts assigned to this computer in typehaus for all users.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, "machine", "run")
	if err != nil {
		return "", err
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		{Type: mgr.NoAction},
	}, 86400)
	if err := s.Start(); err != nil {
		return "", err
	}
	return bin, nil
}

func waitStopped(s *mgr.Service) {
	for i := 0; i < 50; i++ {
		st, err := s.Query()
		if err != nil || st.State == svc.Stopped {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// UninstallService stops and removes the service.
func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	s.Control(svc.Stop)
	waitStopped(s)
	return s.Delete()
}

// ServiceStatus describes the service state.
func ServiceStatus() string {
	m, err := mgr.Connect()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return "not installed"
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "unknown"
	}
	switch st.State {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	}
	return fmt.Sprintf("state %d", st.State)
}

// RunningAsService reports whether the process was started by the service
// control manager.
func RunningAsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

type serviceHandler struct {
	run func(ctx context.Context) error
}

func (h *serviceHandler) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

// RunService runs fn under the Windows service control manager.
func RunService(fn func(ctx context.Context) error) error {
	return svc.Run(ServiceName, &serviceHandler{run: fn})
}

// RestartService restarts the service, which triggers an immediate sync.
func RestartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return errors.New("the typehaus service is not installed – run 'typehaus-agent machine install'")
	}
	defer s.Close()
	s.Control(svc.Stop)
	waitStopped(s)
	return s.Start()
}
