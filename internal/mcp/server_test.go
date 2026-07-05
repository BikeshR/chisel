package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// incomingRequest mirrors request but with Params as raw JSON, so the
// fake server (unlike Server itself, which only ever marshals outbound
// requests) can decode a specific params shape per method.
type incomingRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// TestMain lets the test binary re-exec itself as a minimal, correct MCP
// server over stdio — a real subprocess speaking the real protocol, the
// standard Go pattern for testing exec-based clients without depending on
// an external server or network access.
func TestMain(m *testing.M) {
	if os.Getenv("CHISEL_MCP_FAKE_SERVER") == "1" {
		runFakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeServer implements just enough of MCP to exercise Server: accepts
// initialize, ignores notifications/initialized, answers tools/list with
// one "echo" tool, and tools/call by echoing its arguments back as text
// (or, for the tool name "hang", never responding — for the timeout test).
func runFakeServer() {
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req incomingRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.ID == 0 {
			continue // a notification — nothing to respond to
		}

		var result any
		switch req.Method {
		case "initialize":
			// Only used by TestLoadAndStartAllStartsServersConcurrently —
			// simulates a slow-to-start server (an npx cold download, in
			// practice) so that test can prove multiple servers start in
			// parallel rather than one after another.
			if ms := os.Getenv("CHISEL_MCP_FAKE_SERVER_DELAY_MS"); ms != "" {
				if n, err := strconv.Atoi(ms); err == nil {
					time.Sleep(time.Duration(n) * time.Millisecond)
				}
			}
			result = initializeResult{ProtocolVersion: protocolVersion}
		case "tools/list":
			switch {
			case os.Getenv("CHISEL_MCP_FAKE_SERVER_PAGINATE") == "1":
				// Splits its tool list across two pages — only used by
				// TestServerListToolsFollowsPagination.
				var params struct {
					Cursor string `json:"cursor"`
				}
				_ = json.Unmarshal(req.Params, &params)
				if params.Cursor == "" {
					result = toolsListResult{
						Tools:      []Tool{{Name: "page1tool", InputSchema: map[string]any{"type": "object"}}},
						NextCursor: "page2",
					}
				} else {
					result = toolsListResult{Tools: []Tool{{Name: "page2tool", InputSchema: map[string]any{"type": "object"}}}}
				}
			case os.Getenv("CHISEL_MCP_FAKE_SERVER_STUCK_CURSOR") == "1":
				// Always hands back the exact same cursor, regardless of
				// what was sent — only used by
				// TestServerListToolsDetectsRepeatedCursor.
				result = toolsListResult{
					Tools:      []Tool{{Name: "stucktool", InputSchema: map[string]any{"type": "object"}}},
					NextCursor: "same-cursor-forever",
				}
			case os.Getenv("CHISEL_MCP_FAKE_SERVER_INFINITE_PAGES") == "1":
				// A different cursor every time — never repeats, never
				// terminates. Only used by
				// TestServerListToolsBoundsUnterminatedPagination, to prove
				// the page-count cap (not the repeated-cursor check) is
				// what stops this one.
				var params struct {
					Cursor string `json:"cursor"`
				}
				_ = json.Unmarshal(req.Params, &params)
				n, _ := strconv.Atoi(params.Cursor)
				result = toolsListResult{
					Tools:      []Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}},
					NextCursor: strconv.Itoa(n + 1),
				}
			default:
				result = toolsListResult{Tools: []Tool{
					{Name: "echo", Description: "echoes its input", InputSchema: map[string]any{"type": "object"}},
				}}
			}
		case "resources/list":
			if os.Getenv("CHISEL_MCP_FAKE_SERVER_RESOURCES") == "1" {
				result = resourcesListResult{Resources: []Resource{
					{URI: "file:///notes.txt", Name: "notes", Description: "project notes"},
				}}
			} else {
				result = map[string]any{} // unsupported by this fake server — see Start's graceful-degradation handling
			}
		case "resources/read":
			var params resourcesReadParams
			_ = json.Unmarshal(req.Params, &params)
			result = resourcesReadResult{Contents: []struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text"`
			}{{URI: params.URI, Text: "contents of " + params.URI}}}
		case "prompts/list":
			if os.Getenv("CHISEL_MCP_FAKE_SERVER_PROMPTS") == "1" {
				result = promptsListResult{Prompts: []Prompt{
					{Name: "review", Description: "review code", Arguments: []PromptArgument{{Name: "focus", Required: false}}},
				}}
			} else {
				result = map[string]any{}
			}
		case "prompts/get":
			var params promptsGetParams
			_ = json.Unmarshal(req.Params, &params)
			text := "expanded prompt: " + params.Name
			if focus, ok := params.Arguments["focus"]; ok {
				text += " focused on " + focus
			}
			result = promptsGetResult{Messages: []struct {
				Role    string `json:"role"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}{{Role: "user", Content: struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: text}}}}
		case "tools/call":
			var params toolsCallParams
			_ = json.Unmarshal(req.Params, &params)
			if params.Name == "hang" {
				continue // deliberately never respond
			}
			if os.Getenv("CHISEL_MCP_FAKE_SERVER_PING_COLLISION") == "1" {
				// A server-initiated request with an ID that collides
				// with the call it's about to answer — chisel's own IDs
				// start at 1, so id 1 is exactly what a first real call
				// gets, and exactly what a server's own independent ID
				// space is likely to pick too. Sent just before the real
				// response, only used by
				// TestServerHandlesCollidingServerInitiatedPing.
				ping, _ := json.Marshal(response{JSONRPC: "2.0", ID: req.ID, Method: "ping"})
				_, _ = os.Stdout.Write(append(ping, '\n'))
			}
			result = toolsCallResult{Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: string(params.Arguments)}}}
		default:
			result = map[string]any{}
		}

		resp := response{JSONRPC: "2.0", ID: req.ID}
		data, _ := json.Marshal(result)
		resp.Result = data
		out, _ := json.Marshal(resp)
		_, _ = os.Stdout.Write(append(out, '\n'))
	}
}

func fakeServerConfig() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1"},
	}
}

func fakeServerConfigPaginated() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1", "CHISEL_MCP_FAKE_SERVER_PAGINATE": "1"},
	}
}

func fakeServerConfigStuckCursor() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1", "CHISEL_MCP_FAKE_SERVER_STUCK_CURSOR": "1"},
	}
}

func fakeServerConfigInfinitePages() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1", "CHISEL_MCP_FAKE_SERVER_INFINITE_PAGES": "1"},
	}
}

func fakeServerConfigWithResourcesAndPrompts() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1", "CHISEL_MCP_FAKE_SERVER_RESOURCES": "1", "CHISEL_MCP_FAKE_SERVER_PROMPTS": "1"},
	}
}

func fakeServerConfigPingCollision() ServerConfig {
	return ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"CHISEL_MCP_FAKE_SERVER": "1", "CHISEL_MCP_FAKE_SERVER_PING_COLLISION": "1"},
	}
}

func TestServerStartAndListTools(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	tools := s.Tools()
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("Tools() = %+v, want one tool named echo", tools)
	}
}

// TestServerStartWithoutResourcesOrPromptsSupportDoesNotFail confirms
// the graceful-degradation path: a server that only implements tools
// (the common case — fakeServerConfig's default fake server has no
// resources/prompts handling beyond the generic "unsupported method"
// response) must start successfully with both simply empty, not error.
func TestServerStartWithoutResourcesOrPromptsSupportDoesNotFail(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if len(s.Resources()) != 0 {
		t.Errorf("Resources() = %+v, want empty for a server with no resources/list support", s.Resources())
	}
	if len(s.Prompts()) != 0 {
		t.Errorf("Prompts() = %+v, want empty for a server with no prompts/list support", s.Prompts())
	}
}

func TestServerStartListsResourcesAndPrompts(t *testing.T) {
	s, err := Start("fake", fakeServerConfigWithResourcesAndPrompts())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	resources := s.Resources()
	if len(resources) != 1 || resources[0].URI != "file:///notes.txt" {
		t.Fatalf("Resources() = %+v, want one resource", resources)
	}

	prompts := s.Prompts()
	if len(prompts) != 1 || prompts[0].Name != "review" {
		t.Fatalf("Prompts() = %+v, want one prompt", prompts)
	}
}

func TestServerReadResource(t *testing.T) {
	s, err := Start("fake", fakeServerConfigWithResourcesAndPrompts())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	content, err := s.ReadResource(context.Background(), "file:///notes.txt")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.Contains(content, "file:///notes.txt") {
		t.Errorf("content = %q, want it to reflect the requested URI", content)
	}
}

func TestServerGetPrompt(t *testing.T) {
	s, err := Start("fake", fakeServerConfigWithResourcesAndPrompts())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	text, err := s.GetPrompt(context.Background(), "review", map[string]string{"focus": "security"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if !strings.Contains(text, "review") || !strings.Contains(text, "security") {
		t.Errorf("text = %q, want it to reflect the prompt name and argument", text)
	}
}

// TestServerListToolsFollowsPagination is the regression test for a real
// bug: listTools made exactly one tools/list call and ignored
// nextCursor entirely, so a server that paginates its tool list had
// everything past the first page silently missing, with no error.
func TestServerListToolsFollowsPagination(t *testing.T) {
	s, err := Start("fake", fakeServerConfigPaginated())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	tools := s.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() = %+v, want 2 tools across both pages", tools)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["page1tool"] || !names["page2tool"] {
		t.Errorf("tools = %+v, want both page1tool and page2tool", tools)
	}
}

// TestServerListToolsDetectsRepeatedCursor and
// TestServerListToolsBoundsUnterminatedPagination are the regression
// tests for a real hang: listTools followed nextCursor with no bound at
// all, so a buggy or hostile server that kept returning a cursor would
// make Start (and so chisel's whole startup) loop forever — each
// individual tools/list call is timeout-bounded, but nothing ever
// stopped the loop across calls.
func TestServerListToolsDetectsRepeatedCursor(t *testing.T) {
	_, err := Start("fake", fakeServerConfigStuckCursor())
	if err == nil {
		t.Fatal("expected Start to fail for a server that returns the same cursor twice")
	}
	if !strings.Contains(err.Error(), "same cursor") && !strings.Contains(err.Error(), "stuck") {
		t.Errorf("err = %v, want it to mention the repeated-cursor condition", err)
	}
}

func TestServerListToolsBoundsUnterminatedPagination(t *testing.T) {
	_, err := Start("fake", fakeServerConfigInfinitePages())
	if err == nil {
		t.Fatal("expected Start to fail for a server whose pagination never terminates")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maxToolsListPages)) {
		t.Errorf("err = %v, want it to mention the page cap (%d)", err, maxToolsListPages)
	}
}

func TestServerCallTool(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	content, isError, err := s.CallTool(context.Background(), "echo", json.RawMessage(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if isError {
		t.Error("isError = true, want false")
	}
	if !strings.Contains(content, `"hello":"world"`) {
		t.Errorf("content = %q, want it to echo the arguments", content)
	}
}

// TestServerHandlesCollidingServerInitiatedPing is the regression test
// for a real bug: response has no Method field, so a server-initiated
// request (a "ping" is always legal, regardless of chisel's empty
// declared capabilities) unmarshals as an ordinary response — if its ID
// happens to collide with a pending call's own ID (likely, not exotic:
// chisel's own counter starts at 1, exactly what a first call gets),
// dispatch used to hand the call that ping as if it were its response,
// reporting empty content as success while the real response — read
// later — was silently dropped.
func TestServerHandlesCollidingServerInitiatedPing(t *testing.T) {
	s, err := Start("fake", fakeServerConfigPingCollision())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	content, isError, err := s.CallTool(context.Background(), "echo", json.RawMessage(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if isError {
		t.Error("isError = true, want false")
	}
	if !strings.Contains(content, `"hello":"world"`) {
		t.Errorf("content = %q, want the real response, not the colliding ping mistaken for it", content)
	}
}

// TestServerConcurrentCallsRouteToCorrectCaller hardens the new
// persistent-reader/demux design directly: several goroutines calling
// the same Server concurrently must each get back the response
// matching their own request, dispatched by ID (see readLoop/dispatch),
// not whatever response happens to arrive next on the shared stream.
// Chisel itself dispatches tool calls sequentially today (see CLAUDE.md),
// but the primitive this redesign introduced needs to be correct under
// real concurrency on its own terms, independent of that caller
// discipline.
func TestServerConcurrentCallsRouteToCorrectCaller(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			arg := fmt.Sprintf(`{"n":%d}`, i)
			content, isError, err := s.CallTool(context.Background(), "echo", json.RawMessage(arg))
			if err != nil {
				errCh <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			if isError {
				errCh <- fmt.Errorf("call %d: isError = true", i)
				return
			}
			if !strings.Contains(content, fmt.Sprintf(`"n":%d`, i)) {
				errCh <- fmt.Errorf("call %d got mismatched content: %q", i, content)
				return
			}
			errCh <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}

// TestServerCallToolTimeoutSurvivesAndConnectionRemainsUsable documents
// the behavior after the persistent-reader/demux redesign: the old
// per-call-reader design had to kill the whole connection on any
// timeout, since an abandoned reader racing against the next call's own
// reader on the same stream could desync it. With one persistent reader
// dispatching responses by request ID, a single call timing out only
// fails that one call — the connection, and every call after it,
// carries on unaffected.
func TestServerCallToolTimeoutSurvivesAndConnectionRemainsUsable(t *testing.T) {
	old := callTimeout
	callTimeout = 200 * time.Millisecond
	defer func() { callTimeout = old }()

	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	_, _, err = s.CallTool(context.Background(), "hang", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to mention a timeout", err)
	}

	content, isError, err := s.CallTool(context.Background(), "echo", json.RawMessage(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("CallTool after a prior timeout: %v", err)
	}
	if isError {
		t.Error("expected the call after a timeout to succeed cleanly")
	}
	if !strings.Contains(content, "hello") {
		t.Errorf("content = %q, want the echoed arguments", content)
	}
	if s.broken.Load() {
		t.Error("expected the connection to not be marked broken by a single call's timeout")
	}
}

// TestServerCallToolTimeoutDoesNotKillProcess confirms the process
// itself survives a single call's timeout too — the old design killed
// it unconditionally since the whole connection was considered
// unrecoverable at that point; see broken's own doc comment for why
// that's no longer the case with a persistent reader dispatching by ID.
func TestServerCallToolTimeoutDoesNotKillProcess(t *testing.T) {
	old := callTimeout
	callTimeout = 200 * time.Millisecond
	defer func() { callTimeout = old }()

	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	pid := s.cmd.Process.Pid

	_, _, err = s.CallTool(context.Background(), "hang", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	// Give any (incorrect) kill a moment to land, then confirm the
	// process is still alive and running (not a zombie either).
	time.Sleep(300 * time.Millisecond)
	state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("process %d appears to be gone after a single call's timeout, want it still running: %v", pid, err)
	}
	if fields := strings.Fields(string(state)); len(fields) > 2 && fields[2] == "Z" {
		t.Error("process is a zombie after a single call's timeout, want it still running")
	}
}

// TestServerCallToolInterruptedDoesNotMarkBroken confirms the esc-during
// -a-slow-MCP-call case specifically: cancelling the caller's own ctx
// (not callTimeout expiring) must not brick the connection either, and
// the error text must say "interrupted", not the misleading "timed out
// after 2m0s" a 1-second esc used to produce.
func TestServerCallToolInterruptedDoesNotMarkBroken(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, _, err = s.CallTool(ctx, "hang", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error after the context was cancelled")
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v, want it to say \"interrupted\", not a timeout message", err)
	}
	if s.broken.Load() {
		t.Error("expected a user-cancelled call to not mark the connection broken")
	}

	// The connection must still be usable afterward.
	if _, _, err := s.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); err != nil {
		t.Errorf("CallTool after an interrupted call: %v", err)
	}
}

func TestStartUnknownCommand(t *testing.T) {
	_, err := Start("bad", ServerConfig{Command: "this-binary-does-not-exist-anywhere"})
	if err == nil {
		t.Fatal("expected an error starting a nonexistent command")
	}
}
