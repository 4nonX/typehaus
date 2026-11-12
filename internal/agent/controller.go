package agent

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Phase is the high-level agent state shown in the tray and agent window.
type Phase string

const (
	PhaseDisconnected Phase = "disconnected" // not enrolled
	PhaseStarting     Phase = "starting"     // enrolled, first sync pending
	PhaseSyncing      Phase = "syncing"      // applying changes
	PhaseOK           Phase = "ok"           // in sync
	PhaseOffline      Phase = "offline"      // server unreachable; fonts stay active
	PhaseError        Phase = "error"        // some fonts could not be installed
)

// Status is a snapshot for UIs.
type Status struct {
	Phase    Phase     `json:"phase"`
	Server   string    `json:"server"`
	User     string    `json:"user"`
	DeviceID int64     `json:"device_id"`
	Fonts    int       `json:"fonts"`
	LastSync time.Time `json:"last_sync"`
	Detail   string    `json:"detail"` // error text or notice
}

// Event is an entry in the recent-activity list.
type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // added, removed, error, info
	Text string    `json:"text"`
}

// Controller runs the sync loop for interactive front-ends (tray, agent
// window) and exposes status, activity and actions.
type Controller struct {
	log *slog.Logger

	mu        sync.Mutex
	cfg       *Config
	status    Status
	events    []Event
	listeners []func()
	syncNow   chan struct{}
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewController creates a controller; call Start to begin syncing.
func NewController(log *slog.Logger) *Controller {
	return &Controller{log: log, syncNow: make(chan struct{}, 1), status: Status{Phase: PhaseDisconnected}}
}

// OnChange registers f to be called (in a goroutine-safe way) after every
// status or activity change.
func (c *Controller) OnChange(f func()) {
	c.mu.Lock()
	c.listeners = append(c.listeners, f)
	c.mu.Unlock()
}

func (c *Controller) notify() {
	c.mu.Lock()
	ls := append([]func(){}, c.listeners...)
	c.mu.Unlock()
	for _, f := range ls {
		f()
	}
}

func (c *Controller) update(f func(s *Status)) {
	c.mu.Lock()
	f(&c.status)
	c.mu.Unlock()
	c.notify()
}

func (c *Controller) addEvent(kind, text string) {
	c.mu.Lock()
	c.events = append(c.events, Event{Time: time.Now(), Kind: kind, Text: text})
	if len(c.events) > 100 {
		c.events = c.events[len(c.events)-100:]
	}
	c.mu.Unlock()
}

// Status returns the current status.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Events returns recent activity, newest first.
func (c *Controller) Events() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	for i, e := range c.events {
		out[len(c.events)-1-i] = e
	}
	return out
}

// Installed lists fonts currently installed by the agent, sorted by name.
func (c *Controller) Installed() []*Installed {
	st, err := LoadState()
	if err != nil {
		return nil
	}
	out := []*Installed{}
	for _, f := range st.Fonts {
		if !f.PendingRemoval {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].FullName < out[j].FullName
	})
	return out
}

// Start loads the enrollment (if any) and starts syncing.
func (c *Controller) Start() {
	cfg, err := LoadConfig()
	if err != nil {
		c.update(func(s *Status) { *s = Status{Phase: PhaseDisconnected} })
		return
	}
	c.startLoop(cfg)
}

// Stop ends the sync loop.
func (c *Controller) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// SyncNow triggers an immediate sync.
func (c *Controller) SyncNow() {
	select {
	case c.syncNow <- struct{}{}:
	default:
	}
}

// Enroll connects this computer and starts syncing.
func (c *Controller) Enroll(ctx context.Context, server, code, name string) error {
	cfg, err := Enroll(ctx, server, code, name, "")
	if err != nil {
		return err
	}
	c.Stop()
	c.addEvent("info", "Connected to "+cfg.Server+" as "+cfg.User)
	c.startLoop(cfg)
	return nil
}

// Disconnect removes all typehaus fonts and forgets the enrollment.
func (c *Controller) Disconnect() error {
	c.Stop()
	cfg, err := LoadConfig()
	if err == nil {
		a, err := New(cfg, c.log)
		if err != nil {
			return err
		}
		_, failed, err := a.RemoveAll()
		if err != nil {
			return err
		}
		if failed > 0 {
			c.startLoop(cfg) // keep running: we are still enrolled
			return errors.New("some fonts are in use – close your applications and try again")
		}
	}
	if err := RemoveConfig(); err != nil {
		return err
	}
	c.addEvent("info", "Disconnected; all typehaus fonts were removed")
	c.update(func(s *Status) { *s = Status{Phase: PhaseDisconnected} })
	return nil
}

func (c *Controller) installedCount() int {
	st, err := LoadState()
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range st.Fonts {
		if !f.PendingRemoval {
			n++
		}
	}
	return n
}

func (c *Controller) startLoop(cfg *Config) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	c.cfg = cfg
	c.cancel, c.done = cancel, done
	c.mu.Unlock()
	c.update(func(s *Status) {
		*s = Status{Phase: PhaseStarting, Server: cfg.Server, User: cfg.User, DeviceID: cfg.DeviceID, Fonts: c.installedCount()}
		if st, err := LoadState(); err == nil {
			s.LastSync = st.LastSync
		}
	})
	go func() {
		defer close(done)
		c.loop(ctx, cfg)
	}()
}

func (c *Controller) loop(ctx context.Context, cfg *Config) {
	a, err := New(cfg, c.log)
	if err != nil {
		c.update(func(s *Status) { s.Phase, s.Detail = PhaseError, err.Error() })
		return
	}
	a.OnApply = func(n int) {
		c.update(func(s *Status) { s.Phase = PhaseSyncing })
	}
	interval := 60 * time.Second
	backoff := 0
	for {
		res, err := a.Sync(ctx, false)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrRevoked):
			c.log.Warn("device revoked by server")
			if !cfg.KeepOnRevoke {
				a.RemoveAll()
			}
			RemoveConfig()
			c.addEvent("error", "This computer was disconnected by the server – its typehaus fonts were removed")
			c.update(func(s *Status) {
				*s = Status{Phase: PhaseDisconnected, Detail: "This computer was disconnected by the server."}
			})
			return
		case err != nil:
			backoff = min(backoff+1, 6)
			prev := c.Status().Phase
			if prev != PhaseOffline {
				c.addEvent("error", "Server unreachable: "+err.Error())
			}
			c.update(func(s *Status) { s.Phase, s.Detail = PhaseOffline, err.Error() })
		default:
			backoff = 0
			if res.PollSeconds >= 10 {
				interval = time.Duration(res.PollSeconds) * time.Second
			}
			for _, n := range res.Added {
				c.addEvent("added", n)
			}
			for _, n := range res.Removed {
				c.addEvent("removed", n)
			}
			for _, e := range res.Errors {
				c.addEvent("error", e.Message)
			}
			c.update(func(s *Status) {
				s.Fonts = c.installedCount()
				s.LastSync = time.Now()
				s.Phase, s.Detail = PhaseOK, ""
				if len(res.Errors) > 0 {
					s.Phase = PhaseError
					s.Detail = res.Errors[0].Message
					if len(res.Errors) > 1 {
						s.Detail = res.Errors[0].Message + " (and more)"
					}
				}
			})
		}
		wait := interval
		if backoff > 0 {
			wait = min(time.Duration(1<<backoff)*15*time.Second, 15*time.Minute)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-c.syncNow:
			t.Stop()
			// visible feedback for a manual sync, even when nothing changes
			c.update(func(s *Status) { s.Phase = PhaseSyncing })
			select {
			case <-ctx.Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
		case <-t.C:
		}
	}
}
