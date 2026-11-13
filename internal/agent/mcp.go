package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A minimal client for MCP over the "legacy" HTTP+SSE transport, which is
// what Affinity's built-in MCP server speaks: GET /sse opens an event stream
// whose first "endpoint" event names the URL to POST JSON-RPC messages to;
// the answers arrive as "message" events on the stream.

// mcpProtocol is the only version Affinity accepts (older ones get -32602).
const mcpProtocol = "2025-11-25"

var errSessionLost = errors.New("the MCP session ended")

type mcpSession struct {
	post   string
	hc     *http.Client
	body   io.ReadCloser
	mu     sync.Mutex
	next   int64
	wait   map[int64]chan rpcMessage
	done   chan struct{}
	err    error
	closed bool
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// dialMCP opens a session and initializes it.
func dialMCP(ctx context.Context, sseURL, clientName, clientVersion string) (*mcpSession, error) {
	base, err := url.Parse(sseURL)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 nil, // always direct: the server is on this computer
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableCompression:    true,
	}
	hc := &http.Client{Transport: tr}
	streamCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(streamCtx, "GET", sseURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("%s answered HTTP %d, not an event stream", sseURL, resp.StatusCode)
	}
	s := &mcpSession{hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}, body: resp.Body,
		wait: map[int64]chan rpcMessage{}, done: make(chan struct{})}
	endpoint := make(chan string, 1)
	go func() {
		defer cancel()
		s.read(base, endpoint)
	}()
	select {
	case p := <-endpoint:
		s.post = p
	case <-s.done:
		return nil, fmt.Errorf("the MCP server closed the stream: %v", s.err)
	case <-time.After(5 * time.Second):
		s.Close()
		return nil, errors.New("the MCP server sent no endpoint")
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": clientName, "version": clientVersion},
	}, &init); err != nil {
		s.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := s.notify(ctx, "notifications/initialized", nil); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// read parses the event stream until it ends.
func (s *mcpSession) read(base *url.URL, endpoint chan<- string) {
	sc := bufio.NewScanner(s.body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var event string
	var data []string
	dispatch := func() {
		d := strings.Join(data, "\n")
		ev := event
		event, data = "", nil
		switch ev {
		case "endpoint":
			if u, err := base.Parse(strings.TrimSpace(d)); err == nil {
				select {
				case endpoint <- u.String():
				default:
				}
			}
		case "", "message":
			if d == "" {
				return
			}
			var m rpcMessage
			if json.Unmarshal([]byte(d), &m) != nil {
				return
			}
			if m.Method != "" {
				if len(m.ID) > 0 { // a request from the server (ping): answer it
					go s.send(context.Background(), map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
				}
				return
			}
			id, err := strconv.ParseInt(strings.Trim(string(m.ID), `"`), 10, 64)
			if err != nil {
				return
			}
			s.mu.Lock()
			ch := s.wait[id]
			delete(s.wait, id)
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			dispatch()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	s.finish(err)
}

func (s *mcpSession) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	close(s.done)
}

// Close ends the session.
func (s *mcpSession) Close() {
	s.body.Close()
	s.finish(errSessionLost)
}

// Alive reports whether the stream is still open.
func (s *mcpSession) Alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *mcpSession) send(ctx context.Context, msg any) error {
	b, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", s.post, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode == 404 {
		s.Close()
		return errSessionLost
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("MCP server answered HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *mcpSession) notify(ctx context.Context, method string, params any) error {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		m["params"] = params
	}
	return s.send(ctx, m)
}

// call sends a request and waits for its answer.
func (s *mcpSession) call(ctx context.Context, method string, params, out any) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionLost
	}
	s.next++
	id := s.next
	ch := make(chan rpcMessage, 1)
	s.wait[id] = ch
	s.mu.Unlock()
	forget := func() {
		s.mu.Lock()
		delete(s.wait, id)
		s.mu.Unlock()
	}
	if err := s.send(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		forget()
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-s.done:
		forget()
		return errSessionLost
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
}

// toolText calls a tool and returns its text content.
func (s *mcpSession) toolText(ctx context.Context, name string, args any) (string, bool, error) {
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := s.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), res.IsError, nil
}
