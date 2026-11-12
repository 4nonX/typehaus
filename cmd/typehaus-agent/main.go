// Command typehaus-agent keeps the fonts you activated in typehaus installed
// on this computer for your user account – no administrator rights needed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/4nonX/typehaus/internal/agent"
	"github.com/4nonX/typehaus/internal/agent/tray"
	"github.com/4nonX/typehaus/internal/agent/ui"
)

var version = "dev"

const usage = `typehaus-agent %s – font sync agent for typehaus

Usage:
  typehaus-agent
      (no arguments, e.g. double-click) Start the tray / menu-bar app. If this
      computer isn't connected yet, the typehaus window opens to connect it.
  typehaus-agent tray [--autostart]
      Same as above. --autostart: don't open the window (used at login).
  typehaus-agent enroll --server URL --code CODE [--name NAME] [--ca-file FILE]
      Link this computer to your typehaus account. Get a code in the web UI
      under "Devices".
  typehaus-agent autostart enable|disable
      Start the agent automatically at login (per user, no admin rights).
  typehaus-agent run
      Run headless in the foreground and keep fonts in sync (servers, scripts).
  typehaus-agent sync [--force]
      Sync once and exit.
  typehaus-agent status
      Show enrollment and installed fonts.
  typehaus-agent unenroll
      Remove all fonts installed by typehaus, disable autostart and forget
      this enrollment.

Windows, for all users of this computer (administrator rights required):
  typehaus-agent machine enroll --server URL --code CODE [--name NAME] [--ca-file FILE]
  typehaus-agent machine install     Install and start the typehaus service
  typehaus-agent machine status
  typehaus-agent machine sync        Sync now (restarts the service)
  typehaus-agent machine uninstall   Remove the service, its fonts and the enrollment
      Fonts are installed into %%WINDIR%%\Fonts for every user. Needed only for
      older applications that ignore per-user fonts.
  typehaus-agent version

Environment:
  TYPEHAUS_AGENT_HOME  override the config/state directory
`

func main() {
	agent.Version = version
	if len(os.Args) < 2 {
		if err := cmdTray(false); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprintf(os.Stderr, "\n"+usage, version)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "enroll":
		err = cmdEnroll(ctx, os.Args[2:])
	case "tray":
		err = cmdTray(len(os.Args) > 2 && os.Args[2] == "--autostart")
	case "run":
		err = cmdRun(ctx)
	case "sync":
		err = cmdSync(ctx, os.Args[2:])
	case "status":
		err = cmdStatus()
	case "autostart":
		err = cmdAutostart(os.Args[2:])
	case "unenroll":
		err = cmdUnenroll()
	case "machine":
		err = cmdMachine(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, os.Args[1], version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func consoleLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func cmdEnroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := fs.String("server", "", "typehaus server URL, e.g. https://fonts.example.com")
	code := fs.String("code", "", "enrollment code from the web UI")
	name := fs.String("name", "", "device name shown in the web UI (default: hostname)")
	ca := fs.String("ca-file", "", "PEM file with an additional CA to trust (self-signed / internal CA)")
	fs.Parse(args)
	if *server == "" || *code == "" {
		return errors.New("--server and --code are required")
	}
	if agent.RunningInstance() != nil {
		return errors.New("the typehaus app is running – connect from its window (menu → Open typehaus), or quit it first")
	}
	if c, err := agent.LoadConfig(); err == nil {
		fmt.Printf("Note: replacing existing enrollment for %s on %s\n", c.User, c.Server)
	}
	cfg, err := agent.Enroll(ctx, *server, *code, *name, *ca)
	if err != nil {
		return err
	}
	fmt.Printf("Enrolled as %s on %s (device %d).\n", cfg.User, cfg.Server, cfg.DeviceID)
	a, err := agent.New(cfg, consoleLogger())
	if err != nil {
		return err
	}
	res, err := a.Sync(ctx, true)
	if err != nil {
		return fmt.Errorf("first sync: %w", err)
	}
	fmt.Printf("Synced: %d font file(s) installed.\n", len(res.Added))
	fmt.Println("Next: run 'typehaus-agent autostart enable' to keep fonts in sync automatically.")
	return nil
}

// fileLogger logs to the agent log file (and the console, if attached).
func fileLogger() (*slog.Logger, func()) {
	var out io.Writer = os.Stderr
	closer := func() {}
	if lp, err := agent.LogPath(); err == nil {
		os.MkdirAll(filepath.Dir(lp), 0o700)
		if fi, err := os.Stat(lp); err == nil && fi.Size() > 5<<20 {
			os.Rename(lp, lp+".1")
		}
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			closer = func() { f.Close() }
			if hasConsole() {
				out = io.MultiWriter(os.Stderr, f)
			} else {
				out = f
			}
		}
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo})), closer
}

// cmdTray runs the tray / menu-bar app with the agent window.
func cmdTray(autostart bool) error {
	// Already running? Bring up its window instead of starting twice –
	// unless this is a different version (an update): then take over.
	if in := agent.RunningInstance(); in != nil {
		if in.Version == version {
			return agent.OpenURL(in.URL)
		}
		in.Replace()
	}
	detachConsole()
	release, err := agent.WritePID()
	if err != nil {
		return err
	}
	defer release()
	log, closeLog := fileLogger()
	defer closeLog()

	c := agent.NewController(log)
	win, err := ui.Start(c)
	if err != nil {
		return err
	}
	agent.PublishInstance(win.URL())
	defer agent.ClearInstance()
	// until the tray (or background loop) takes over, a quit request exits
	win.OnQuit = func() {
		c.Stop()
		win.Close()
		agent.ClearInstance()
		release()
		os.Exit(0)
	}
	agent.RefreshAutostart() // a newly downloaded version replaces the login copy
	c.Start()

	// At login the panel may not be ready yet (Linux); give it time. Syncing
	// and the window already work meanwhile.
	wait := 3 * time.Second
	if autostart {
		wait = 90 * time.Second
	}
	hasTray := tray.WaitAvailable(wait)
	log.Info("typehaus started", "version", version, "os", runtime.GOOS, "tray", hasTray)

	if hasTray {
		return tray.Run(c, win, log, tray.Options{OpenWindowIfDisconnected: !autostart})
	}
	// No tray host (e.g. GNOME without the AppIndicator extension): keep
	// syncing in the background; the window is reachable by launching
	// typehaus again.
	log.Warn("no system tray available – running in the background; launch typehaus again to open its window")
	if !autostart {
		agent.OpenURL(win.URL())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	win.OnQuit = stop
	<-ctx.Done()
	c.Stop()
	win.Close()
	return nil
}

func cmdRun(ctx context.Context) error {
	cfg, err := agent.LoadConfig()
	if err != nil {
		return err
	}
	release, err := agent.WritePID()
	if err != nil {
		return err
	}
	defer release()
	log, closeLog := fileLogger()
	defer closeLog()
	log.Info("typehaus agent starting", "version", version, "server", cfg.Server, "user", cfg.User, "os", runtime.GOOS)
	a, err := agent.New(cfg, log)
	if err != nil {
		return err
	}
	err = a.Run(ctx)
	if errors.Is(err, agent.ErrRevoked) {
		if !cfg.KeepOnRevoke {
			agent.RemoveConfig()
		}
		log.Info("enrollment removed; enroll again to resume syncing")
		return nil // clean exit: service managers must not restart-loop
	}
	return err
}

func hasConsole() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func cmdSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	force := fs.Bool("force", false, "re-download the manifest and verify every installed file")
	fs.Parse(args)
	if in := agent.RunningInstance(); in != nil {
		if err := in.Call("sync"); err != nil {
			return fmt.Errorf("the typehaus app is running but did not respond: %w", err)
		}
		fmt.Println("Asked the running typehaus app to sync. See its window for the result.")
		return nil
	}
	cfg, err := agent.LoadConfig()
	if err != nil {
		return err
	}
	a, err := agent.New(cfg, consoleLogger())
	if err != nil {
		return err
	}
	res, err := a.Sync(ctx, *force)
	if err != nil {
		return err
	}
	if res.Unchanged {
		fmt.Println("Up to date.")
		return nil
	}
	for _, n := range res.Added {
		fmt.Println("+", n)
	}
	for _, n := range res.Removed {
		fmt.Println("-", n)
	}
	for _, e := range res.Errors {
		fmt.Println("!", e.SHA256[:12], e.Message)
	}
	fmt.Printf("Done: %d added, %d removed, %d errors.\n", len(res.Added), len(res.Removed), len(res.Errors))
	return nil
}

func cmdStatus() error {
	cfg, err := agent.LoadConfig()
	if err != nil {
		return err
	}
	st, err := agent.LoadState()
	if err != nil {
		return err
	}
	dir, _ := agent.ConfigDir()
	fmt.Printf("Server:     %s\nUser:       %s\nDevice ID:  %d\nConfig dir: %s\n", cfg.Server, cfg.User, cfg.DeviceID, dir)
	if !st.LastSync.IsZero() {
		fmt.Printf("Last sync:  %s (%s ago)\n", st.LastSync.Format(time.RFC3339), time.Since(st.LastSync).Round(time.Second))
	} else {
		fmt.Println("Last sync:  never")
	}
	if st.LastErr != "" {
		fmt.Println("Last error:", st.LastErr)
	}
	var names []string
	pending := 0
	for _, f := range st.Fonts {
		if f.PendingRemoval {
			pending++
			continue
		}
		names = append(names, f.FullName)
	}
	sort.Strings(names)
	fmt.Printf("Installed:  %d font file(s)", len(names))
	if pending > 0 {
		fmt.Printf(", %d waiting for removal (in use)", pending)
	}
	fmt.Println()
	for _, n := range names {
		fmt.Println("  ", n)
	}
	return nil
}

func cmdAutostart(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: typehaus-agent autostart enable|disable")
	}
	switch args[0] {
	case "enable":
		if _, err := agent.LoadConfig(); err != nil {
			return err
		}
		mode := "run"
		if tray.Available() {
			mode = "tray"
		}
		msg, err := agent.EnableAutostart(mode, agent.RunningInstance() == nil)
		if err != nil {
			return err
		}
		fmt.Println("Autostart enabled:", msg)
	case "disable":
		if err := agent.DisableAutostart(true); err != nil {
			return err
		}
		fmt.Println("Autostart disabled.")
	default:
		return errors.New("usage: typehaus-agent autostart enable|disable")
	}
	return nil
}

func cmdUnenroll() error {
	if in := agent.RunningInstance(); in != nil {
		in.Replace() // quit the tray app so it doesn't re-install fonts
	}
	agent.DisableAutostart(true)
	cfg, err := agent.LoadConfig()
	if err != nil {
		return err
	}
	a, err := agent.New(cfg, consoleLogger())
	if err != nil {
		return err
	}
	removed, failed, err := a.RemoveAll()
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d font file(s).\n", removed)
	if failed > 0 {
		return fmt.Errorf("%d font file(s) are in use – close your applications and run 'typehaus-agent unenroll' again", failed)
	}
	if err := agent.RemoveConfig(); err != nil {
		return err
	}
	fmt.Println("Enrollment removed. Revoke the device in the web UI if it is still listed.")
	return nil
}

// ---- machine-wide mode (Windows) ------------------------------------------

func cmdMachine(ctx context.Context, args []string) error {
	if !agent.MachineSupported() {
		agent.SetMachineMode()
		_, err := agent.InstallService() // returns the platform explanation
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: typehaus-agent machine enroll|install|status|sync|uninstall")
	}
	agent.SetMachineMode()
	if args[0] == "run" {
		if agent.RunningAsService() {
			return agent.RunService(runMachine)
		}
		return runMachine(ctx)
	}
	if !agent.IsElevated() {
		return errors.New("administrator rights required – run this from an elevated terminal (\"Run as administrator\")")
	}
	switch args[0] {
	case "enroll":
		fs := flag.NewFlagSet("machine enroll", flag.ExitOnError)
		server := fs.String("server", "", "typehaus server URL")
		code := fs.String("code", "", "enrollment code from the web UI")
		name := fs.String("name", "", "device name (default: hostname)")
		ca := fs.String("ca-file", "", "PEM file with an additional CA to trust")
		fs.Parse(args[1:])
		if *server == "" || *code == "" {
			return errors.New("--server and --code are required")
		}
		host, _ := os.Hostname()
		if *name == "" {
			*name = host + " (all users)"
		}
		cfg, err := agent.Enroll(ctx, *server, *code, *name, *ca)
		if err != nil {
			return err
		}
		fmt.Printf("Enrolled this computer for all users as %s on %s (device %d).\n", cfg.User, cfg.Server, cfg.DeviceID)
		fmt.Println("The fonts activated by this account will be installed for every user of this computer.")
		fmt.Println("Next: typehaus-agent machine install")
		return nil
	case "install":
		if _, err := agent.LoadConfig(); err != nil {
			return errors.New("not enrolled yet – run 'typehaus-agent machine enroll' first")
		}
		bin, err := agent.InstallService()
		if err != nil {
			return err
		}
		fmt.Printf("Service %q installed and started (%s).\n", agent.ServiceName, bin)
		return nil
	case "status":
		fmt.Println("Service:   ", agent.ServiceStatus())
		if cfg, err := agent.LoadConfig(); err == nil {
			fmt.Printf("Server:     %s\nAccount:    %s\nDevice ID:  %d\n", cfg.Server, cfg.User, cfg.DeviceID)
		} else {
			fmt.Println("Enrollment: none")
		}
		if st, err := agent.LoadState(); err == nil {
			if !st.LastSync.IsZero() {
				fmt.Printf("Last sync:  %s\n", st.LastSync.Format(time.RFC3339))
			}
			if st.LastErr != "" {
				fmt.Println("Last error:", st.LastErr)
			}
			n := 0
			for _, f := range st.Fonts {
				if !f.PendingRemoval {
					n++
				}
			}
			fmt.Printf("Installed:  %d font file(s) for all users\n", n)
		}
		return nil
	case "sync":
		if err := agent.RestartService(); err != nil {
			return err
		}
		fmt.Println("Service restarted – it syncs right away. Check 'typehaus-agent machine status'.")
		return nil
	case "uninstall":
		if err := agent.UninstallService(); err != nil {
			return err
		}
		cfg, err := agent.LoadConfig()
		if err == nil {
			a, err := agent.New(cfg, consoleLogger())
			if err != nil {
				return err
			}
			removed, failed, err := a.RemoveAll()
			if err != nil {
				return err
			}
			fmt.Printf("Removed %d font file(s) for all users", removed)
			if failed > 0 {
				fmt.Printf(" (%d in use – deleted at the next restart)", failed)
			}
			fmt.Println(".")
		}
		if err := agent.RemoveConfig(); err != nil {
			return err
		}
		fmt.Println("Service removed and enrollment forgotten. Revoke the device in the web UI if it is still listed.")
		return nil
	}
	return fmt.Errorf("unknown machine command %q", args[0])
}

// runMachine is the service body: sync loop with logging to the protected
// machine directory.
func runMachine(ctx context.Context) error {
	log, closeLog := fileLogger()
	defer closeLog()
	cfg, err := agent.LoadConfig()
	if err != nil {
		log.Error("machine service: not enrolled", "err", err)
		return err
	}
	log.Info("typehaus machine service starting", "version", version, "server", cfg.Server, "account", cfg.User)
	a, err := agent.New(cfg, log)
	if err != nil {
		return err
	}
	err = a.Run(ctx)
	if errors.Is(err, agent.ErrRevoked) {
		agent.RemoveConfig()
		log.Info("device revoked – fonts removed, enrollment forgotten")
		return nil
	}
	return err
}
