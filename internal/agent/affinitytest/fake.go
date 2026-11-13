// Package affinitytest emulates Affinity's built-in MCP server for tests:
// the legacy HTTP+SSE transport, protocol 2025-11-25 only, the "preamble"
// gate in front of execute_script, sessions that can be lost, and a
// document model answering typehaus's font script. Behaviour follows the
// measurements of Affinity 3.3.1 published by the talk-to-affinity-mcp
// project (specs/000-spike/research.md).
package affinitytest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

const gate = "The preamble documentation topic has not yet been read."

// Font is a font a document uses; Installed is what Affinity reports.
type Font struct {
	Name      string
	Installed bool
}

// Doc is an open document.
type Doc struct {
	Title   string
	Path    string
	Current bool
	Fonts   []Font
}

// Fake is a running emulation.
type Fake struct {
	URL string // the /sse address

	// RunScript, if set, executes scripts (e.g. in Node with SDK stubs);
	// otherwise the fake answers typehaus's font script from Docs.
	RunScript func(script string) string

	mu       sync.Mutex
	docs     []Doc
	sessions map[string]*session
	srv      *http.Server
	ln       net.Listener
	calls    []string
	scripts  int
	connects int
}

type session struct {
	out  chan []byte
	gate bool
	done chan struct{}
	once sync.Once
}

func (s *session) end() { s.once.Do(func() { close(s.done) }) }

// Start listens on 127.0.0.1 (a random port, or addr if given).
func Start(addr string) (*Fake, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f := &Fake{ln: ln, sessions: map[string]*session{}, URL: "http://" + ln.Addr().String() + "/sse"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", f.stream)
	mux.HandleFunc("POST /message", f.message)
	f.srv = &http.Server{Handler: mux}
	go f.srv.Serve(ln)
	return f, nil
}

// SetDocs replaces the open documents.
func (f *Fake) SetDocs(docs ...Doc) {
	f.mu.Lock()
	f.docs = docs
	f.mu.Unlock()
}

// SetInstalled changes what Affinity reports for a font in every document.
func (f *Fake) SetInstalled(name string, installed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.docs {
		for j := range f.docs[i].Fonts {
			if f.docs[i].Fonts[j].Name == name {
				f.docs[i].Fonts[j].Installed = installed
			}
		}
	}
}

// Calls lists the tool calls received ("tools/call:<name>" or a method).
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// Scripts is how many scripts ran.
func (f *Fake) Scripts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scripts
}

// Connects is how many event streams were opened.
func (f *Fake) Connects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

// Restart ends every event stream, as quitting Affinity does.
func (f *Fake) Restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.sessions {
		s.end()
		delete(f.sessions, id)
	}
}

// ForgetSessions loses the sessions but leaves the streams open: messages
// then get HTTP 404.
func (f *Fake) ForgetSessions() {
	f.mu.Lock()
	f.sessions = map[string]*session{}
	f.mu.Unlock()
}

// Close stops the server.
func (f *Fake) Close() {
	f.Restart()
	f.srv.Close()
}

func (f *Fake) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)
	s := &session{out: make(chan []byte, 64), done: make(chan struct{})}
	f.mu.Lock()
	f.sessions[id] = s
	f.connects++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fmt.Fprintf(w, "event: endpoint\ndata: /message?session_id=%s\n\n", id)
	fl.Flush()
	for {
		select {
		case m := <-s.out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", m)
			fl.Flush()
		case <-s.done:
			return
		case <-r.Context().Done():
			f.mu.Lock()
			delete(f.sessions, id)
			f.mu.Unlock()
			return
		}
	}
}

type rpc struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

func (f *Fake) message(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	s := f.sessions[r.URL.Query().Get("session_id")]
	f.mu.Unlock()
	if s == nil {
		http.Error(w, "Session not found", 404)
		return
	}
	var m rpc
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	w.WriteHeader(202)
	w.Write([]byte("Accepted"))
	if m.Method == "" {
		return // an answer from the client (to a ping)
	}
	f.mu.Lock()
	name := m.Method
	if m.Method == "tools/call" {
		var p struct {
			Name string `json:"name"`
		}
		json.Unmarshal(m.Params, &p)
		name += ":" + p.Name
	}
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if len(m.ID) == 0 {
		return // a notification
	}
	go func() {
		res, rerr := f.handle(s, m)
		msg := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = res
		}
		b, _ := json.Marshal(msg)
		select {
		case s.out <- b:
		case <-s.done:
		}
		if m.Method == "initialize" {
			// Affinity may ping; the client must answer
			p, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "srv-ping", "method": "ping"})
			select {
			case s.out <- p:
			case <-s.done:
			}
		}
	}()
}

func text(t string, isErr bool) map[string]any {
	out := map[string]any{"content": []map[string]string{{"type": "text", "text": t}}}
	if isErr {
		out["isError"] = true
	}
	return out
}

func (f *Fake) handle(s *session, m rpc) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		if p.ProtocolVersion != "2025-11-25" {
			return nil, map[string]any{"code": -32602, "message": "Unsupported protocol version"}
		}
		return map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo":   map[string]string{"name": "Affinity", "version": "1.0.0"},
			"instructions": "You MUST read the 'preamble' documentation before using the SDK."}, nil
	case "tools/list":
		return map[string]any{"tools": []map[string]any{
			{"name": "execute_script", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"script": map[string]string{"type": "string"}}, "required": []string{"script"}}},
			{"name": "read_sdk_documentation_topic", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"filename": map[string]string{"type": "string"}}, "required": []string{"filename"}}},
		}}, nil
	case "tools/call":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		json.Unmarshal(m.Params, &p)
		switch p.Name {
		case "read_sdk_documentation_topic":
			if p.Arguments["filename"] == "preamble" {
				f.mu.Lock()
				s.gate = true // opens even though the read fails, as under Wine
				f.mu.Unlock()
				return text("File not found", true), nil
			}
			return text("ERROR: "+gate, false), nil
		case "execute_script":
			f.mu.Lock()
			open := s.gate
			if open {
				f.scripts++
			}
			run := f.RunScript
			f.mu.Unlock()
			if !open {
				return text(gate, true), nil
			}
			if run != nil {
				return text(run(p.Arguments["script"]), false), nil
			}
			return text(f.answer(p.Arguments["script"]), false), nil
		}
		return nil, map[string]any{"code": -32602, "message": "Unknown tool"}
	case "ping":
		return map[string]any{}, nil
	}
	return nil, map[string]any{"code": -32601, "message": "Method not found"}
}

var nonceRe = regexp.MustCompile(`const N = '([^']+)'`)

// answer emulates typehaus's font script against the documents.
func (f *Fake) answer(script string) string {
	if !strings.Contains(script, "enumerateFontNames") || !strings.Contains(script, "documents.all") {
		return "ReferenceError: unexpected script\n    at main:1:1\n"
	}
	n := nonceRe.FindStringSubmatch(script)
	if n == nil {
		return "SyntaxError: no nonce\n    at main:1:1\n"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	type doc struct {
		Title   string   `json:"title"`
		Path    string   `json:"path"`
		Current bool     `json:"current"`
		Fonts   [][2]any `json:"fonts"`
	}
	docs := []doc{}
	for _, d := range f.docs {
		x := doc{Title: d.Title, Path: d.Path, Current: d.Current, Fonts: [][2]any{}}
		for _, ft := range d.Fonts {
			x.Fonts = append(x.Fonts, [2]any{ft.Name, ft.Installed})
		}
		docs = append(docs, x)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "version": "Affinity 3.3.1.4940 (fake)", "docs": docs})
	return n[1] + string(b) + "\n"
}
