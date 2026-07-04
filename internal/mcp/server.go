package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// callTimeout bounds a single tools/call. Unlike a fresh subprocess per
// call, a hung MCP server tool has nothing else to bound it — a var, not
// a const, so tests can shorten it.
var callTimeout = 2 * time.Minute

// Server is a running connection to one configured MCP server: a
// subprocess speaking newline-delimited JSON-RPC 2.0 over its stdin/
// stdout, per the MCP stdio transport spec. Server's own stderr is
// discarded — chisel's TUI owns the terminal via an alt-screen, so
// letting an arbitrary server write to it directly would corrupt
// rendering; there's no debug surface for it in this first version.
type Server struct {
	name string

	cmd   *exec.Cmd
	stdin io.WriteCloser

	// mu guards nextID and pending — the persistent readLoop goroutine
	// (started once, in Start) and every call to Call/call itself can
	// touch both concurrently, unlike the old one-reader-goroutine-per-
	// call design where each call owned its own read.
	mu      sync.Mutex
	nextID  int
	pending map[int]chan readResult

	tools []Tool

	// broken is set once the connection itself is known to be
	// unusable — a read failure (EOF, a malformed line) or a failed
	// write, meaning the framing itself may be desynced. atomic since
	// it's written from readLoop's own goroutine and read from
	// CallTool/Statuses (View's render path, syncMCPHealth at every
	// turn) concurrently. Deliberately *not* set just because one call
	// timed out or was cancelled — with a single persistent reader
	// dispatching by request ID (see readLoop), an abandoned call
	// doesn't desync anything for the calls that come after it, so
	// there's no need to tear down the whole connection over one slow
	// or cancelled request the way the old per-call-reader design had
	// to. There's no automatic reconnect in this version; restarting
	// chisel reconnects.
	broken atomic.Bool
}

// readResult is what readLoop hands back to whichever call() is
// waiting for a given request ID — exactly one of resp/err is set.
type readResult struct {
	resp response
	err  error
}

// Start spawns cfg's command, performs the MCP initialize handshake, and
// fetches the server's tool list.
func Start(name string, cfg ServerConfig) (*Server, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", cfg.Command, err)
	}

	s := &Server{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int]chan readResult),
	}
	// ReadBytes accumulates past one internal buffer's worth, so this
	// handles arbitrarily long lines. One persistent reader for the
	// life of the connection — see readLoop's own doc comment for why.
	go s.readLoop(bufio.NewReader(stdout))

	if err := s.handshake(); err != nil {
		s.Close()
		return nil, fmt.Errorf("handshake with %s: %w", name, err)
	}

	tools, err := s.listTools()
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("list tools from %s: %w", name, err)
	}
	s.tools = tools

	return s, nil
}

// Tools returns the server's tools, in its own (unprefixed) naming.
func (s *Server) Tools() []Tool { return s.tools }

// Close shuts down the server process. Safe to call more than once.
func (s *Server) Close() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// CallTool invokes name (the server's own unprefixed name) with arguments
// and returns its combined text content.
func (s *Server) CallTool(ctx context.Context, name string, arguments json.RawMessage) (content string, isError bool, err error) {
	if s.broken.Load() {
		return "", true, fmt.Errorf("mcp server %s: connection is no longer usable — restart chisel to reconnect", s.name)
	}

	var result toolsCallResult
	if err := s.call(ctx, "tools/call", toolsCallParams{Name: name, Arguments: arguments}, &result); err != nil {
		return "", true, err
	}

	var b strings.Builder
	for i, c := range result.Content {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(c.Text)
	}
	return b.String(), result.IsError, nil
}

func (s *Server) handshake() error {
	var result initializeResult
	err := s.call(context.Background(), "initialize", initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfo{Name: "chisel", Version: "0.1"},
	}, &result)
	if err != nil {
		return err
	}
	return s.notify("notifications/initialized", nil)
}

// listTools fetches every tool the server offers, following nextCursor
// across as many tools/list calls as the server actually paginates
// into. Without this, a server whose tool list spans more than one page
// silently lost every tool past the first page, with no error or any
// other indication something was cut off.
// maxToolsListPages bounds how many pages listTools will follow via
// nextCursor — each individual call is timeout-bounded, but a
// buggy or hostile server that keeps returning a non-empty cursor
// forever would otherwise make startup hang in this loop indefinitely,
// one timeout-bounded call at a time.
const maxToolsListPages = 100

func (s *Server) listTools() ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxToolsListPages {
			return nil, fmt.Errorf("tools/list did not terminate after %d pages (server keeps returning a cursor)", maxToolsListPages)
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result toolsListResult
		if err := s.call(context.Background(), "tools/list", params, &result); err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if result.NextCursor == "" {
			break
		}
		if result.NextCursor == cursor {
			return nil, fmt.Errorf("tools/list returned the same cursor twice (%q) — server appears stuck", cursor)
		}
		cursor = result.NextCursor
	}
	return tools, nil
}

// call sends a JSON-RPC request and waits for the response with a
// matching ID, via readLoop's dispatch rather than reading the shared
// stream itself — see readLoop's own doc comment for why that matters:
// a call that times out or is cancelled here just stops waiting, it
// doesn't affect the connection or any other in-flight/future call.
func (s *Server) call(ctx context.Context, method string, params, result any) error {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan readResult, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.send(request{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		s.removePending(id)
		s.markBroken()
		return fmt.Errorf("write %s request: %w", method, err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if r.resp.Error != nil {
			return r.resp.Error
		}
		if result != nil && r.resp.Result != nil {
			return json.Unmarshal(r.resp.Result, result)
		}
		return nil
	case <-timeoutCtx.Done():
		s.removePending(id)
		// ctx (the caller's own, e.g. a turn cancelled by esc) is what's
		// actually done here, not just timeoutCtx's derived deadline —
		// distinguishing the two matters for the same reason
		// bashsession.go's own ctx.Err() check does: a 1-second esc and
		// a genuine callTimeout timeout must not read as the same
		// condition. Neither bricks the connection — see broken's own
		// doc comment for why that's no longer necessary here.
		if ctx.Err() != nil {
			return fmt.Errorf("%s interrupted", method)
		}
		return fmt.Errorf("%s timed out after %s", method, callTimeout)
	}
}

func (s *Server) removePending(id int) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

// readLoop is the single, persistent reader of the server's stdout —
// started once in Start and running for the life of the connection.
// Every response is dispatched to whichever call() is waiting for that
// request ID via s.pending, rather than each call spawning its own
// reader on the shared stream the old design used: two goroutines
// racing to read the same pipe (one abandoned after a timeout or
// cancellation, one for the next call) risked one consuming a response
// meant for the other, which is exactly why the old design killed the
// whole process on any timeout just to avoid that ever happening. With
// one reader dispatching by ID, an abandoned call simply stops
// listening — the connection, and every other in-flight or future call,
// is unaffected by it.
func (s *Server) readLoop(reader *bufio.Reader) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			s.markBroken()
			s.failAllPending(fmt.Errorf("read response: %w", err))
			return
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			// A malformed line desyncs the framing for everything after
			// it too, not just this one response — same treatment as a
			// read failure.
			s.markBroken()
			s.failAllPending(fmt.Errorf("decode response: %w", err))
			return
		}
		s.dispatch(resp)
	}
}

// dispatch hands resp to whichever call is waiting for its ID, if any —
// chisel doesn't support server-initiated requests in this version, and
// a response to a call that already timed out/was cancelled has no
// listener left, so it's dropped in either case.
func (s *Server) dispatch(resp response) {
	s.mu.Lock()
	ch, ok := s.pending[resp.ID]
	if ok {
		delete(s.pending, resp.ID)
	}
	s.mu.Unlock()
	if ok {
		ch <- readResult{resp: resp}
	}
}

// failAllPending delivers err to every call still waiting on a
// response, once readLoop itself has died — without this, a call whose
// response never arrives (because the connection is gone) would block
// until its own callTimeout expires instead of failing immediately.
func (s *Server) failAllPending(err error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[int]chan readResult)
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- readResult{err: err}
	}
}

// markBroken tears down the underlying process once the connection
// itself is known to be unusable (readLoop exited, or a write failed) —
// not just because one call timed out or was cancelled, see broken's
// own doc comment. Killing the process closes its stdout, which is what
// makes readLoop's own read return so it can call this and
// failAllPending in turn if it wasn't the one that triggered this in
// the first place (e.g. a failed write here, versus EOF over there).
//
// Deliberately not calling cmd.Wait() here, even though that leaves the
// process a zombie until Close() (called once, at chisel shutdown)
// eventually reaps it: exec.Cmd.Wait is documented as unsafe to call
// concurrently, and Close already calls it — calling it again from here
// too, possibly at the same time, would just trade one race for another.
func (s *Server) markBroken() {
	if s.broken.Swap(true) {
		return
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *Server) notify(method string, params any) error {
	return s.send(request{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.stdin.Write(data)
	return err
}
