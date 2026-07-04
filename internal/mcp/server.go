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

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	nextID int
	tools  []Tool

	// broken is set once any protocol-level error occurs (timeout, EOF,
	// malformed response) — after that, the connection is in an unknown
	// state and every further call fails fast rather than risking a read
	// desynced from a previous hung request. There's no automatic
	// reconnect in this version; restarting chisel reconnects.
	broken bool
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
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReader(stdout), // ReadBytes accumulates past one internal buffer's worth, so this handles arbitrarily long lines
	}

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
	if s.broken {
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

func (s *Server) listTools() ([]Tool, error) {
	var result toolsListResult
	if err := s.call(context.Background(), "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// call sends a JSON-RPC request and waits for the response with a
// matching ID, discarding anything else read in between (a notification,
// or a stale response to an abandoned request) — chisel doesn't support
// server-initiated requests in this version.
func (s *Server) call(ctx context.Context, method string, params, result any) error {
	s.nextID++
	id := s.nextID
	if err := s.send(request{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		s.broken = true
		return fmt.Errorf("write %s request: %w", method, err)
	}

	type readResult struct {
		resp response
		err  error
	}
	done := make(chan readResult, 1)

	go func() {
		for {
			line, err := s.reader.ReadBytes('\n')
			if err != nil {
				done <- readResult{err: fmt.Errorf("read response: %w", err)}
				return
			}
			var resp response
			if err := json.Unmarshal(line, &resp); err != nil {
				done <- readResult{err: fmt.Errorf("decode response: %w", err)}
				return
			}
			if resp.ID != id {
				continue
			}
			done <- readResult{resp: resp}
			return
		}
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	select {
	case r := <-done:
		if r.err != nil {
			s.broken = true
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
		s.broken = true
		return fmt.Errorf("%s timed out after %s", method, callTimeout)
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
