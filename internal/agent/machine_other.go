//go:build !windows

package agent

import (
	"context"
	"errors"
)

// Machine-wide installation exists to serve older Windows applications that
// ignore per-user fonts. macOS and Linux applications see per-user fonts, so
// there is no machine mode there.

var machineMode bool

// ServiceName is the Windows service name (machine-wide mode).
const ServiceName = "typehaus"

var errNoMachineMode = errors.New("machine-wide mode is only available on Windows – applications on macOS and Linux see per-user fonts")

func SetMachineMode()                                  {}
func MachineSupported() bool                           { return false }
func machineDir() (string, error)                      { return "", errNoMachineMode }
func publishMachineFonts(*State)                       {}
func MachineInstalled() map[string]bool                { return map[string]bool{} }
func LoadMachineFontsIntoSession()                     {}
func IsElevated() bool                                 { return false }
func InstallService() (string, error)                  { return "", errNoMachineMode }
func UninstallService() error                          { return errNoMachineMode }
func ServiceStatus() string                            { return "not available on this platform" }
func RunningAsService() bool                           { return false }
func RunService(func(ctx context.Context) error) error { return errNoMachineMode }
func RestartService() error                            { return errNoMachineMode }
