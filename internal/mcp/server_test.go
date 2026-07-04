package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
			result = initializeResult{ProtocolVersion: protocolVersion}
		case "tools/list":
			result = toolsListResult{Tools: []Tool{
				{Name: "echo", Description: "echoes its input", InputSchema: map[string]any{"type": "object"}},
			}}
		case "tools/call":
			var params toolsCallParams
			_ = json.Unmarshal(req.Params, &params)
			if params.Name == "hang" {
				continue // deliberately never respond
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

func TestServerCallToolTimeout(t *testing.T) {
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

	// A broken connection fails fast on the next call rather than trying
	// to read from a stream desynced by the still-pending hung request.
	_, _, err = s.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "restart chisel") {
		t.Errorf("expected the second call to fail fast with a reconnect message, got: %v", err)
	}
}

func TestServerCallToolTimeoutKillsProcess(t *testing.T) {
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

	// markBroken kills the process but deliberately doesn't Wait() on it
	// (that's Close's job, called once at shutdown — see markBroken's own
	// comment for why) — so right after a timeout it's a zombie, not
	// fully gone. Checking /proc directly for that Z state confirms it
	// was actually killed rather than left running and leaked; reading
	// s.cmd.ProcessState here instead would race with Close's later Wait().
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return // already reaped somehow — also fine, definitely not leaked running
		}
		if fields := strings.Fields(string(state)); len(fields) > 2 && fields[2] == "Z" {
			return // zombie: killed, awaiting reap at Close — not leaked running
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process was still running 2s after a timeout — it's leaked, not killed")
}

func TestStartUnknownCommand(t *testing.T) {
	_, err := Start("bad", ServerConfig{Command: "this-binary-does-not-exist-anywhere"})
	if err == nil {
		t.Fatal("expected an error starting a nonexistent command")
	}
}
