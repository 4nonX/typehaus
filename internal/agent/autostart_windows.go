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
const runValue = "typehaus-agent"

// EnableAutostart registers the agent to start at logon (HKCU Run key) and
// starts it in the background now.
func EnableAutostart() (string, error) {
	StopRunning()
	bin, err := installBinary()
	if err != nil {
		return "", err
	}
	note := ""
	if !strings.HasSuffix(strings.ToLower(bin), "typehaus-agentw.exe") {
		note = "\nnote: typehaus-agentw.exe was not found next to the agent – a console window will open at logon"
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	if err := k.SetStringValue(runValue, `"`+bin+`" run`); err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000008} // CREATE_NO_WINDOW | DETACHED_PROCESS
	if err := cmd.Start(); err == nil {
		cmd.Process.Release()
	}
	return "registered in HKCU\\" + runKey + " and started " + bin + note, nil
}

// DisableAutostart removes the logon entry and stops the background agent.
func DisableAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err == nil {
		defer k.Close()
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	StopRunning()
	return nil
}
