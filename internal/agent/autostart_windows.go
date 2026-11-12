//go:build windows

package agent

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "typehaus"

// EnableAutostart registers the agent to start at logon (HKCU Run key).
// mode is "tray" or "run". With startNow, a background instance is launched.
func EnableAutostart(mode string, startNow bool) (string, error) {
	if startNow {
		StopRunning()
	}
	bin, err := installBinary()
	if err != nil {
		return "", err
	}
	note := ""
	if !strings.HasSuffix(strings.ToLower(bin), "typehaus-agentw.exe") && mode == "run" {
		note = "\nnote: typehaus-agentw.exe was not found next to the agent – a console window will open at logon"
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	k.DeleteValue("typehaus-agent") // name used by v0.1
	if err := k.SetStringValue(runValue, `"`+bin+`" `+autostartArgs(mode)); err != nil {
		return "", err
	}
	if startNow {
		cmd := exec.Command(bin, mode)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000008} // CREATE_NO_WINDOW | DETACHED_PROCESS
		if err := cmd.Start(); err == nil {
			cmd.Process.Release()
		}
	}
	return "starts at logon from " + bin + note, nil
}

// DisableAutostart removes the logon entry. With stop, a running
// background instance is terminated.
func DisableAutostart(stop bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err == nil {
		defer k.Close()
		for _, v := range []string{runValue, "typehaus-agent"} {
			if err := k.DeleteValue(v); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
		}
	}
	if stop {
		StopRunning()
	}
	return nil
}

// InstallNotice tells the user how to fix an installation problem, if any.
func InstallNotice() string { return "" }

// AutostartEnabled reports whether a logon entry exists.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	for _, v := range []string{runValue, "typehaus-agent"} {
		if _, _, err := k.GetStringValue(v); err == nil {
			return true
		}
	}
	return false
}
