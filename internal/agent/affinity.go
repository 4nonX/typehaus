package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/4nonX/typehaus/internal/protocol"
)

// The Affinity bridge: Affinity (by Canva, 3.3 and later) has no plugin
// interface, but a built-in MCP server (Settings > Model Context Protocol >
// Enable MCP server). typehaus connects to it, reads the fonts of the open
// documents with a read-only SDK script, and asks the typehaus server about
// the ones Affinity reports missing. Fonts the person may use are switched
// on (automatically, or with a click in the typehaus window); the rest can be
// requested.
//
// Facts this relies on (measured against Affinity 3.3.1 by the
// talk-to-affinity-mcp project): the server listens on 127.0.0.1:6767 (on
// macOS sometimes only on [::1]); execute_script refuses to run until the
// "preamble" documentation topic was read once in the session; only console
// output comes back; doc.enumerateFontNames((name, installed) => …) lists the
// PostScript names a document uses, and a callback returning a truthy value
// ends the listing.

// AffinityMode is what the bridge does with missing fonts.
type AffinityMode string

const (
	AffinityAuto AffinityMode = "auto" // switch available fonts on by itself
	AffinityShow AffinityMode = "show" // show them; switch on with a click
	AffinityOff  AffinityMode = "off"  // don't connect to Affinity
)

var affinityURLs = []string{"http://127.0.0.1:6767/sse", "http://[::1]:6767/sse"}

// AffinityFont is one font an open document uses.
type AffinityFont struct {
	PostScript string `json:"postscript"`
	// InAffinity: Affinity has the font (installed by anyone).
	InAffinity bool `json:"in_affinity"`
	// what typehaus can do (only asked for fonts Affinity is missing):
	// active, available, refused, unsupported, not_available; "" = not asked
	Status    string `json:"status,omitempty"`
	Family    string `json:"family,omitempty"`
	Style     string `json:"style,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Installed bool   `json:"installed,omitempty"` // typehaus installed the file here
	FontID    int64  `json:"-"`
	SHA256    string `json:"-"`
}

// AffinityDocument is an open document.
type AffinityDocument struct {
	Title   string         `json:"title"`
	Path    string         `json:"path,omitempty"`
	Current bool           `json:"current"`
	Fonts   []AffinityFont `json:"fonts"`
}

// AffinityState is what the typehaus window shows.
type AffinityState struct {
	Mode      AffinityMode       `json:"mode"`
	Connected bool               `json:"connected"`
	Version   string             `json:"version,omitempty"`
	Message   string             `json:"message,omitempty"` // why not connected, or a script problem
	Checked   time.Time          `json:"checked"`
	Documents []AffinityDocument `json:"documents"`
}

// Missing counts fonts Affinity lacks in all open documents.
func (st AffinityState) Missing() int {
	n := 0
	for _, d := range st.Documents {
		for _, f := range d.Fonts {
			if !f.InAffinity {
				n++
			}
		}
	}
	return n
}

// AffinityBridge watches Affinity while typehaus runs.
type AffinityBridge struct {
	log      *slog.Logger
	agent    func() (*Agent, error)
	syncNow  func()
	event    func(kind, text string)
	changed  func()
	urls     []string
	interval time.Duration

	mu        sync.Mutex
	state     AffinityState
	sess      *mcpSession
	nextTry   time.Time
	tried     map[string]time.Time // PostScript name → last automatic activation
	asked     map[string]cachedStatus
	wake      chan struct{}
	cancel    context.CancelFunc
	done      chan struct{}
	preambled bool
}

type cachedStatus struct {
	st protocol.FontStatus
	at time.Time
}

// NewAffinityBridge prepares the bridge; event and changed may be nil.
func NewAffinityBridge(log *slog.Logger, agent func() (*Agent, error), syncNow func(), event func(kind, text string), changed func()) *AffinityBridge {
	urls := affinityURLs
	if u := strings.TrimSpace(os.Getenv("TYPEHAUS_AFFINITY_URL")); u != "" {
		urls = []string{u}
	}
	if event == nil {
		event = func(string, string) {}
	}
	if changed == nil {
		changed = func() {}
	}
	return &AffinityBridge{log: log, agent: agent, syncNow: syncNow, event: event, changed: changed, urls: urls,
		interval: 4 * time.Second, tried: map[string]time.Time{}, asked: map[string]cachedStatus{},
		wake: make(chan struct{}, 1), state: AffinityState{Mode: LoadAffinityMode(), Documents: []AffinityDocument{}}}
}

// ---- settings ----------------------------------------------------------------

// LoadAffinityMode reads the setting (default: auto). TYPEHAUS_AFFINITY
// overrides it.
func LoadAffinityMode() AffinityMode {
	switch m := AffinityMode(strings.ToLower(os.Getenv("TYPEHAUS_AFFINITY"))); m {
	case AffinityAuto, AffinityShow, AffinityOff:
		return m
	}
	switch p := loadPrefs(); p.Affinity {
	case AffinityShow, AffinityOff:
		return p.Affinity
	}
	return AffinityAuto
}

// SetMode changes and stores the setting.
func (b *AffinityBridge) SetMode(m AffinityMode) error {
	if m != AffinityAuto && m != AffinityShow && m != AffinityOff {
		return errors.New("mode must be auto, show or off")
	}
	path, err := prefsPath()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	p := loadPrefs()
	p.Affinity = m
	if err := writeJSONFile(path, p, 0o600); err != nil {
		return err
	}
	b.mu.Lock()
	b.state.Mode = m
	if m == AffinityOff {
		b.dropLocked()
		b.state.Connected, b.state.Documents, b.state.Message = false, []AffinityDocument{}, ""
	}
	b.nextTry = time.Time{}
	b.mu.Unlock()
	b.Check()
	b.changed()
	return nil
}

// State returns a snapshot.
func (b *AffinityBridge) State() AffinityState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state
	st.Documents = append([]AffinityDocument{}, b.state.Documents...)
	return st
}

// CheckOnce connects, reads the open documents and asks the server about
// missing fonts – without switching anything on – then disconnects.
func (b *AffinityBridge) CheckOnce(ctx context.Context) AffinityState {
	b.mu.Lock()
	b.state.Mode = AffinityShow
	b.mu.Unlock()
	b.round(ctx)
	b.mu.Lock()
	b.dropLocked()
	b.mu.Unlock()
	return b.State()
}

// Check asks for a round right away.
func (b *AffinityBridge) Check() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// ---- loop -------------------------------------------------------------------

// Start runs the bridge until Stop.
func (b *AffinityBridge) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	if b.cancel != nil {
		b.mu.Unlock()
		cancel()
		return
	}
	b.cancel, b.done = cancel, make(chan struct{})
	done := b.done
	b.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(b.interval)
		defer t.Stop()
		for {
			b.round(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-b.wake:
			}
		}
	}()
}

// Stop ends the bridge.
func (b *AffinityBridge) Stop() {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.cancel, b.done = nil, nil
	b.dropLocked()
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (b *AffinityBridge) dropLocked() {
	if b.sess != nil {
		b.sess.Close()
		b.sess = nil
	}
	b.preambled = false
}

const notRunning = "Affinity isn't running, or its MCP server is off. In Affinity: Settings > Model Context Protocol > Enable MCP server, then restart Affinity."

func (b *AffinityBridge) setState(f func(st *AffinityState)) {
	b.mu.Lock()
	before, _ := json.Marshal(b.state)
	f(&b.state)
	after, _ := json.Marshal(b.state)
	b.mu.Unlock()
	if string(before) != string(after) {
		b.changed()
	}
}

func (b *AffinityBridge) round(ctx context.Context) {
	b.mu.Lock()
	mode := b.state.Mode
	b.mu.Unlock()
	if mode == AffinityOff {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	docs, version, err := b.readDocuments(ctx)
	if err != nil {
		b.setState(func(st *AffinityState) {
			st.Connected, st.Version, st.Documents = false, "", []AffinityDocument{}
			st.Message = err.Error()
			st.Checked = time.Now()
		})
		return
	}
	b.resolve(ctx, docs)
	if mode == AffinityAuto {
		if n := b.activate(ctx, docs, nil); n > 0 {
			b.resolveFresh(ctx, docs)
		}
	}
	b.setState(func(st *AffinityState) {
		st.Connected, st.Version, st.Message, st.Documents, st.Checked = true, version, "", docs, time.Now()
	})
}

// session returns an open, initialized session, connecting when needed.
func (b *AffinityBridge) session(ctx context.Context) (*mcpSession, error) {
	b.mu.Lock()
	s := b.sess
	if s != nil && !s.Alive() {
		b.dropLocked()
		s = nil
	}
	wait := time.Until(b.nextTry)
	b.mu.Unlock()
	if s != nil {
		return s, nil
	}
	if wait > 0 {
		return nil, errors.New(notRunning)
	}
	var errs []string
	for _, u := range b.urls {
		dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		s, err := dialMCP(dctx, u, "typehaus", Version)
		cancel()
		if err == nil {
			b.mu.Lock()
			b.sess, b.preambled = s, false
			b.mu.Unlock()
			b.log.Info("connected to Affinity", "url", u)
			return s, nil
		}
		errs = append(errs, err.Error())
	}
	b.mu.Lock()
	b.nextTry = time.Now().Add(10 * time.Second)
	b.mu.Unlock()
	b.log.Debug("Affinity not reachable", "errors", strings.Join(errs, "; "))
	return nil, errors.New(notRunning)
}

// gateText is Affinity's answer before the preamble was read.
func gated(text string) bool {
	return strings.Contains(text, "preamble documentation topic has not yet been read")
}

// runScript executes a script in Affinity and returns its console output.
func (b *AffinityBridge) runScript(ctx context.Context, script string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		s, err := b.session(ctx)
		if err != nil {
			return "", err
		}
		b.mu.Lock()
		need := !b.preambled
		b.mu.Unlock()
		if need {
			// opens the gate even when the read itself fails (no network)
			if _, _, err := s.toolText(ctx, "read_sdk_documentation_topic", map[string]string{"filename": "preamble"}); err != nil {
				b.mu.Lock()
				b.dropLocked()
				b.mu.Unlock()
				continue
			}
			b.mu.Lock()
			b.preambled = true
			b.mu.Unlock()
		}
		out, isErr, err := s.toolText(ctx, "execute_script", map[string]string{"script": script})
		if err != nil {
			b.mu.Lock()
			b.dropLocked()
			b.mu.Unlock()
			if errors.Is(err, errSessionLost) || ctx.Err() == nil {
				continue // a fresh session, once
			}
			return "", err
		}
		if gated(out) {
			b.mu.Lock()
			b.preambled = false
			b.mu.Unlock()
			continue
		}
		if isErr {
			return "", fmt.Errorf("Affinity refused the script: %s", firstLine(out))
		}
		return out, nil
	}
	return "", errors.New("Affinity stopped answering – typehaus keeps trying")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// affinityScript lists the open documents and their fonts. Read only: it
// changes nothing in Affinity. Output: one line, the nonce and JSON.
const affinityScript = `const N = '__NONCE__';
try {
  const { app } = require('/application.js');
  const cur = app.documents.current;
  const docs = [];
  for (const d of app.documents.all) {
    const fonts = [];
    d.enumerateFontNames((name, installed) => {
      fonts.push([String(name), !!installed]);
    });
    docs.push({
      title: String(d.title || ''),
      path: d.path ? String(d.path) : '',
      current: !!cur && String(d.sessionUuid) === String(cur.sessionUuid),
      fonts: fonts,
    });
  }
  console.log(N + JSON.stringify({ ok: true, version: String(app.version || ''), docs: docs }));
} catch (e) {
  console.log(N + JSON.stringify({ ok: false, error: String((e && e.message) || e) }));
}
`

type scriptDocs struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Version string `json:"version"`
	Docs    []struct {
		Title   string   `json:"title"`
		Path    string   `json:"path"`
		Current bool     `json:"current"`
		Fonts   [][2]any `json:"fonts"`
	} `json:"docs"`
}

func newNonce() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "TYPEHAUS-" + hex.EncodeToString(b) + ":"
}

// parseScriptDocs reads the script's output line.
func parseScriptDocs(out, nonce string) ([]AffinityDocument, string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, nonce) {
			continue
		}
		var r scriptDocs
		if err := json.Unmarshal([]byte(line[len(nonce):]), &r); err != nil {
			return nil, "", fmt.Errorf("unexpected answer from Affinity: %v", err)
		}
		if !r.OK {
			return nil, "", fmt.Errorf("Affinity couldn't list the fonts: %s", r.Error)
		}
		docs := []AffinityDocument{}
		for _, d := range r.Docs {
			doc := AffinityDocument{Title: d.Title, Path: d.Path, Current: d.Current, Fonts: []AffinityFont{}}
			seen := map[string]bool{}
			for _, f := range d.Fonts {
				name, _ := f[0].(string)
				in, _ := f[1].(bool)
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				doc.Fonts = append(doc.Fonts, AffinityFont{PostScript: name, InAffinity: in})
			}
			sort.SliceStable(doc.Fonts, func(i, j int) bool {
				if doc.Fonts[i].InAffinity != doc.Fonts[j].InAffinity {
					return !doc.Fonts[i].InAffinity
				}
				return doc.Fonts[i].PostScript < doc.Fonts[j].PostScript
			})
			docs = append(docs, doc)
		}
		return docs, r.Version, nil
	}
	return nil, "", fmt.Errorf("Affinity couldn't run the font check: %s", firstLine(out))
}

func (b *AffinityBridge) readDocuments(ctx context.Context) ([]AffinityDocument, string, error) {
	nonce := newNonce()
	out, err := b.runScript(ctx, strings.Replace(affinityScript, "__NONCE__", nonce, 1))
	if err != nil {
		return nil, "", err
	}
	return parseScriptDocs(out, nonce)
}

// resolve asks the server about the fonts Affinity is missing (answers are
// kept for a minute).
func (b *AffinityBridge) resolve(ctx context.Context, docs []AffinityDocument) {
	b.lookup(ctx, docs, time.Minute)
}

func (b *AffinityBridge) resolveFresh(ctx context.Context, docs []AffinityDocument) {
	b.lookup(ctx, docs, 0)
}

func (b *AffinityBridge) lookup(ctx context.Context, docs []AffinityDocument, maxAge time.Duration) {
	var ask []protocol.FontRef
	seen := map[string]bool{}
	b.mu.Lock()
	for _, d := range docs {
		for _, f := range d.Fonts {
			if f.InAffinity || seen[f.PostScript] {
				continue
			}
			seen[f.PostScript] = true
			if c, ok := b.asked[f.PostScript]; !ok || time.Since(c.at) > maxAge || maxAge == 0 {
				ask = append(ask, protocol.FontRef{PostScript: f.PostScript})
			}
		}
	}
	b.mu.Unlock()
	if len(ask) > 0 {
		if a, err := b.agent(); err == nil {
			if st, err := a.Resolve(ctx, ask); err == nil {
				b.mu.Lock()
				for i, s := range st {
					if i < len(ask) {
						b.asked[ask[i].PostScript] = cachedStatus{st: s, at: time.Now()}
					}
				}
				b.mu.Unlock()
			} else {
				b.log.Warn("Affinity fonts: server lookup failed", "err", err)
			}
		}
	}
	have := installedSHAs()
	b.mu.Lock()
	defer b.mu.Unlock()
	for di := range docs {
		for fi := range docs[di].Fonts {
			f := &docs[di].Fonts[fi]
			if f.InAffinity {
				continue
			}
			c, ok := b.asked[f.PostScript]
			if !ok {
				continue
			}
			f.Status, f.Family, f.Style, f.Reason, f.FontID, f.SHA256 = c.st.Status, c.st.Family, c.st.Style, c.st.Reason, c.st.FontID, c.st.SHA256
			f.Installed = f.SHA256 != "" && have[f.SHA256]
		}
	}
}

// activate switches on the fonts with status "available" – only those in
// only (PostScript names) when given. Automatic rounds don't retry a font
// within ten minutes.
func (b *AffinityBridge) activate(ctx context.Context, docs []AffinityDocument, only map[string]bool) int {
	var ids []int64
	var names []string
	used := map[int64]bool{}
	where := map[string]string{}
	b.mu.Lock()
	for _, d := range docs {
		for _, f := range d.Fonts {
			if f.Status != "available" || used[f.FontID] {
				continue
			}
			if only != nil && !only[f.PostScript] {
				continue
			}
			if only == nil && time.Since(b.tried[f.PostScript]) < 10*time.Minute {
				continue
			}
			used[f.FontID] = true
			b.tried[f.PostScript] = time.Now()
			ids = append(ids, f.FontID)
			names = append(names, strings.TrimSpace(f.Family+" "+f.Style))
			where[f.PostScript] = d.Title
		}
	}
	b.mu.Unlock()
	if len(ids) == 0 {
		return 0
	}
	a, err := b.agent()
	if err != nil {
		return 0
	}
	if err := a.ActivateFonts(ctx, ids); err != nil {
		b.event("error", "Affinity: couldn't switch on "+strings.Join(names, ", ")+": "+err.Error())
		return 0
	}
	docsNamed := map[string]bool{}
	for _, d := range docs {
		for _, f := range d.Fonts {
			if _, ok := where[f.PostScript]; ok && !f.InAffinity {
				docsNamed[d.Title] = true
			}
		}
	}
	var titles []string
	for t := range docsNamed {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	b.event("info", fmt.Sprintf("Affinity: switched on %s for %s", strings.Join(names, ", "), strings.Join(titles, ", ")))
	b.log.Info("Affinity: activated fonts", "count", len(ids), "documents", strings.Join(titles, ", "))
	b.mu.Lock()
	for ps := range where {
		delete(b.asked, ps)
	}
	b.mu.Unlock()
	if b.syncNow != nil {
		b.syncNow()
	}
	return len(ids)
}

// ActivateMissing switches on the available fonts of the open documents
// (all of them, or the PostScript names given) and checks again.
func (b *AffinityBridge) ActivateMissing(ctx context.Context, names []string) (int, error) {
	b.mu.Lock()
	docs := append([]AffinityDocument{}, b.state.Documents...)
	b.mu.Unlock()
	only := map[string]bool{}
	for _, n := range names {
		only[n] = true
	}
	if len(names) == 0 {
		for _, d := range docs {
			for _, f := range d.Fonts {
				only[f.PostScript] = true
			}
		}
	}
	n := b.activate(ctx, docs, only)
	b.Check()
	return n, nil
}

// Request asks the administrators for a font a document uses.
func (b *AffinityBridge) Request(ctx context.Context, postscript, document string) error {
	a, err := b.agent()
	if err != nil {
		return errors.New("typehaus isn't connected on this computer")
	}
	purpose := "Used in an Affinity document"
	if document != "" {
		purpose = "Used in the Affinity document “" + document + "”"
	}
	if err := a.RequestFont(ctx, postscript, purpose, "Affinity"); err != nil {
		if strings.Contains(err.Error(), "already requested") {
			return nil // asked before: that's what the person wants
		}
		return err
	}
	b.event("info", "Requested "+postscript)
	return nil
}
