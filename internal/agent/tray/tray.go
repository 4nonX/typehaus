//go:build windows || linux || (darwin && cgo)

// Package tray shows the typehaus status icon and menu in the Windows
// notification area, the macOS menu bar or a Linux StatusNotifier panel.
package tray

import (
	"fmt"
	"log/slog"
	"net/url"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/4nonX/typehaus/internal/agent"
	"github.com/4nonX/typehaus/internal/agent/ui"
	"github.com/4nonX/typehaus/internal/brand"
)

// Options configure the tray.
type Options struct {
	// OpenWindowIfDisconnected shows the agent window on start when this
	// computer is not connected yet (first launch). Off for login autostart.
	OpenWindowIfDisconnected bool
}

type labels struct {
	open, library, sync, login, quit, connect, about, update string
}

// Windows and Linux use sentence case, macOS title case.
func menuLabels() labels {
	if runtime.GOOS == "darwin" {
		return labels{"Open typehaus", "Open Font Library", "Sync Now", "Start at Login", "Quit typehaus", "Connect to Server…", "typehaus %s", "Update Available – typehaus %s…"}
	}
	return labels{"Open typehaus", "Open font library", "Sync now", "Start at login", "Quit typehaus", "Connect to server…", "typehaus %s", "Update available – typehaus %s…"}
}

// Run shows the tray and blocks until the user quits. It must be called on
// the main goroutine (macOS requirement).
func Run(c *agent.Controller, win *ui.Server, log *slog.Logger, opts Options) error {
	if !Available() {
		return ErrUnavailable
	}
	t := &trayApp{c: c, win: win, log: log, opts: opts, L: menuLabels()}
	win.OnQuit = t.quitApp
	systray.Run(t.ready, func() {})
	return nil
}

type trayApp struct {
	c    *agent.Controller
	win  *ui.Server
	log  *slog.Logger
	opts Options
	L    labels

	mu      sync.Mutex
	frame   int
	iconKey string

	status, detail, open, library, sync, login, about, update, quit *systray.MenuItem
	quitOnce                                                        sync.Once
}

func (t *trayApp) ready() {
	systray.SetTitle("")
	t.applyIcon(brand.StateDisconnected, 0)
	systray.SetTooltip("typehaus")

	t.status = systray.AddMenuItem("typehaus", "")
	t.status.Disable()
	t.detail = systray.AddMenuItem("", "")
	t.detail.Disable()
	systray.AddSeparator()
	t.open = systray.AddMenuItem(t.L.open, "Status, installed fonts and settings")
	t.library = systray.AddMenuItem(t.L.library, "Browse and activate fonts in your browser")
	t.sync = systray.AddMenuItem(t.L.sync, "Check the server for changes now")
	systray.AddSeparator()
	t.login = systray.AddMenuItemCheckbox(t.L.login, "Start typehaus when you log in", agent.AutostartEnabled())
	systray.AddSeparator()
	t.update = systray.AddMenuItem("", "Download the new version from your typehaus server")
	t.update.Hide()
	t.about = systray.AddMenuItem(fmt.Sprintf(t.L.about, agent.Version), "")
	t.about.Disable()
	t.quit = systray.AddMenuItem(t.L.quit, "Fonts stay installed until you disconnect")

	if runtime.GOOS == "windows" {
		systray.SetOnTapped(t.showWindow) // left click opens the window, right click the menu
	}

	changed := make(chan struct{}, 1)
	t.c.OnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	t.refresh()

	go t.clicks()
	go func() {
		anim := time.NewTicker(450 * time.Millisecond)
		slow := time.NewTicker(20 * time.Second)
		defer anim.Stop()
		defer slow.Stop()
		for {
			select {
			case <-changed:
				t.refresh()
			case <-anim.C:
				if p := t.c.Status().Phase; p == agent.PhaseSyncing || p == agent.PhaseStarting {
					t.mu.Lock()
					t.frame++
					t.mu.Unlock()
					t.refreshIcon()
				}
			case <-slow.C:
				// relative times in the menu and theme changes (Windows light/dark taskbar)
				t.refresh()
			}
		}
	}()

	if t.opts.OpenWindowIfDisconnected && t.c.Status().Phase == agent.PhaseDisconnected {
		go func() {
			time.Sleep(400 * time.Millisecond)
			t.showWindow()
		}()
	}
}

func (t *trayApp) clicks() {
	for {
		select {
		case <-t.open.ClickedCh:
			t.showWindow()
		case <-t.library.ClickedCh:
			if s := t.c.Status().Server; s != "" {
				agent.OpenURL(s)
			}
		case <-t.sync.ClickedCh:
			t.c.SyncNow()
		case <-t.login.ClickedCh:
			if t.login.Checked() {
				if err := agent.DisableAutostart(false); err != nil {
					t.log.Error("disable autostart", "err", err)
				}
			} else if _, err := agent.EnableAutostart("tray", false); err != nil {
				t.log.Error("enable autostart", "err", err)
			}
			t.syncLoginCheck()
		case <-t.update.ClickedCh:
			if u := t.c.Update(); u.Available {
				agent.OpenURL(u.URL)
			}
		case <-t.quit.ClickedCh:
			t.quitApp()
			return
		}
	}
}

func (t *trayApp) quitApp() {
	t.quitOnce.Do(func() {
		t.c.Stop()
		t.win.Close()
		agent.ClearInstance()
		systray.Quit()
	})
}

func (t *trayApp) showWindow() {
	if err := agent.OpenURL(t.win.URL()); err != nil {
		t.log.Error("open window", "err", err)
	}
}

func (t *trayApp) syncLoginCheck() {
	if agent.AutostartEnabled() {
		t.login.Check()
	} else {
		t.login.Uncheck()
	}
}

func phaseState(p agent.Phase) brand.State {
	switch p {
	case agent.PhaseOK:
		return brand.StateOK
	case agent.PhaseSyncing, agent.PhaseStarting:
		return brand.StateSyncing
	case agent.PhaseOffline:
		return brand.StateOffline
	case agent.PhaseError:
		return brand.StateError
	}
	return brand.StateDisconnected
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 15*time.Second:
		return "just now"
	case d < time.Minute:
		return "less than a minute ago"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute", "minutes") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour", "hours") + " ago"
	}
	return t.Format("2 Jan 15:04")
}

func (t *trayApp) refresh() {
	st := t.c.Status()
	var head, sub, tip string
	switch st.Phase {
	case agent.PhaseOK:
		head = "Up to date · " + plural(st.Fonts, "font", "fonts")
		sub = "Checked " + ago(st.LastSync)
		tip = "typehaus – up to date, " + plural(st.Fonts, "font", "fonts") + " active"
	case agent.PhaseSyncing:
		head, tip = "Syncing fonts…", "typehaus – syncing"
	case agent.PhaseStarting:
		head, tip = "Connecting…", "typehaus – connecting"
	case agent.PhaseOffline:
		head = "Offline – fonts stay active"
		sub = "Last synced " + ago(st.LastSync)
		tip = "typehaus – server unreachable"
	case agent.PhaseError:
		head = "Some fonts need attention"
		sub = st.Detail
		tip = "typehaus – some fonts could not be installed"
	case agent.PhaseSuspended:
		head, tip = "Account disabled – fonts removed", "typehaus – account disabled"
		sub = "Fonts return when your account is enabled"
	default:
		head, tip = "Not connected", "typehaus – not connected"
		sub = st.Detail
	}
	if st.Server != "" && (st.Phase == agent.PhaseSyncing || st.Phase == agent.PhaseStarting) {
		sub = st.User + " · " + hostOf(st.Server)
	}
	if len(sub) > 60 {
		sub = sub[:57] + "…"
	}
	t.status.SetTitle(head)
	if sub == "" {
		t.detail.Hide()
	} else {
		t.detail.SetTitle(sub)
		t.detail.Show()
	}
	systray.SetTooltip(tip)

	if u := t.c.Update(); u.Available {
		t.update.SetTitle(fmt.Sprintf(t.L.update, u.Version))
		t.update.Show()
	} else {
		t.update.Hide()
	}

	connected := st.Phase != agent.PhaseDisconnected
	if connected {
		t.open.SetTitle(t.L.open)
		t.library.Show()
		t.sync.Show()
		if st.Phase == agent.PhaseSyncing {
			t.sync.Disable()
		} else {
			t.sync.Enable()
		}
	} else {
		t.open.SetTitle(t.L.connect)
		t.library.Hide()
		t.sync.Hide()
	}
	t.syncLoginCheck()
	t.refreshIcon()
}

func (t *trayApp) refreshIcon() {
	t.mu.Lock()
	frame := t.frame
	t.mu.Unlock()
	t.applyIcon(phaseState(t.c.Status().Phase), frame)
}

func (t *trayApp) applyIcon(s brand.State, frame int) {
	if s != brand.StateSyncing {
		frame = 0
	}
	key := fmt.Sprintf("%d/%d/%s", s, frame%2, iconVariantKey())
	t.mu.Lock()
	same := key == t.iconKey
	t.iconKey = key
	t.mu.Unlock()
	if !same {
		setIcon(s, frame)
	}
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
