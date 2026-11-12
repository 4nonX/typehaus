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
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/4nonX/typehaus/internal/agent"
)

var version = "dev"

const usage = `typehaus-agent %s – font sync agent for typehaus

Usage:
  typehaus-agent enroll --server URL --code CODE [--name NAME] [--ca-file FILE]
      Link this computer to your typehaus account. Get a code in the web UI
      under "Devices".
  typehaus-agent autostart enable|disable
      Start the agent automatically at login (per user, no admin rights).
  typehaus-agent run
      Run in the foreground and keep fonts in sync.
  typehaus-agent sync [--force]
      Sync once and exit.
  typehaus-agent status
      Show enrollment and installed fonts.
  typehaus-agent unenroll
      Remove all fonts installed by typehaus, disable autostart and forget
      this enrollment.
  typehaus-agent version

Environment:
  TYPEHAUS_AGENT_HOME  override the config/state directory
`

func main() {
	agent.Version = version
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "enroll":
		err = cmdEnroll(ctx, os.Args[2:])
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

	var out io.Writer = os.Stderr
	if lp, err := agent.LogPath(); err == nil {
		if fi, err := os.Stat(lp); err == nil && fi.Size() > 5<<20 {
			os.Rename(lp, lp+".1")
		}
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			if hasConsole() {
				out = io.MultiWriter(os.Stderr, f)
			} else {
				out = f
			}
		}
	}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
		msg, err := agent.EnableAutostart()
		if err != nil {
			return err
		}
		fmt.Println("Autostart enabled:", msg)
	case "disable":
		if err := agent.DisableAutostart(); err != nil {
			return err
		}
		fmt.Println("Autostart disabled.")
	default:
		return errors.New("usage: typehaus-agent autostart enable|disable")
	}
	return nil
}

func cmdUnenroll() error {
	agent.DisableAutostart()
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
