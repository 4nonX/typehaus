package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// installBinary copies the running executable (and, on Windows, a sibling
// windowless variant) into a stable per-user location and returns the path
// that autostart should launch.
func installBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, _ = filepath.EvalSymlinks(self)
	dir, err := binDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	install := func(src, name string) (string, error) {
		dst := filepath.Join(dir, name)
		if !sameFile(src, dst) {
			if err := copyFile(src, dst); err != nil {
				return "", fmt.Errorf("copy %s: %w", name, err)
			}
		}
		return dst, nil
	}
	if runtime.GOOS != "windows" {
		return install(self, "typehaus-agent")
	}
	// Windows: the console binary is used for the CLI; a windowless variant
	// (typehaus-agentw*.exe, downloaded alongside) is preferred for autostart.
	console := self
	var headless string
	if strings.HasPrefix(strings.ToLower(filepath.Base(self)), "typehaus-agentw") {
		headless = self
	} else if m, _ := filepath.Glob(filepath.Join(filepath.Dir(self), "typehaus-agentw*.exe")); len(m) > 0 {
		headless = m[0]
	}
	launched, err := install(console, "typehaus-agent.exe")
	if err != nil {
		return "", err
	}
	if headless != "" {
		return install(headless, "typehaus-agentw.exe")
	}
	return launched, nil
}

func sameFile(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

func binDir() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bin"), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// On Windows a running exe cannot be overwritten but it can be renamed away.
	if _, err := os.Stat(dst); err == nil {
		old := dst + ".old"
		os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// autostartArgs are the arguments a login launch uses. "--autostart" keeps
// the tray from popping up the window at every login.
func autostartArgs(mode string) string {
	if mode == "tray" {
		return "tray --autostart"
	}
	return mode
}

// LogPath is where the background agent writes its log.
func LogPath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "agent.log"), err
}

func pidPath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "agent.pid"), err
}

// WritePID records the running agent's PID. It fails if another agent
// process for this user is already running.
func WritePID() (release func(), err error) {
	p, err := pidPath()
	if err != nil {
		return nil, err
	}
	if pid := readPID(p); pid > 0 && pid != os.Getpid() && processAlive(pid) {
		return nil, fmt.Errorf("another typehaus agent is already running (pid %d)", pid)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, err
	}
	return func() {
		if readPID(p) == os.Getpid() {
			os.Remove(p)
		}
	}, nil
}

// StopRunning terminates a background agent started for this user, if any.
func StopRunning() {
	p, err := pidPath()
	if err != nil {
		return
	}
	pid := readPID(p)
	if pid <= 0 || pid == os.Getpid() || !processAlive(pid) {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Kill()
	}
	os.Remove(p)
}

func readPID(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}
