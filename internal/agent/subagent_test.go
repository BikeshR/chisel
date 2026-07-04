package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunViewFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runView(dir, json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("runView: %v", err)
	}
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Errorf("output = %q", out)
	}
}

func TestRunViewRejectsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	// A NUL byte early in the content is what the isBinary/looksBinary
	// heuristic keys on — matching grep's own existing binary-file skip.
	data := append([]byte("PNG"), 0x00, 0x01, 0x02, 0x03)
	if err := os.WriteFile(filepath.Join(dir, "image.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runView(dir, json.RawMessage(`{"path":"image.png"}`))
	if err == nil {
		t.Error("expected an error viewing a binary file, got nil")
	}
}

func TestRunViewDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runView(dir, json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("runView: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Errorf("output = %q", out)
	}
}

func TestRunViewRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := runView(dir, json.RawMessage(`{"path":"../../etc/passwd"}`))
	if err == nil {
		t.Error("expected an error escaping the working directory")
	}
}

// fakeMCPStyleServer builds a streaming chat-completions server that
// scripts a fixed sequence of responses per call, for driving
// RunSubagent's loop deterministically without a real model.
func scriptedServer(t *testing.T, responses []string) (*httptest.Server, *int32) {
	t.Helper()
	var call int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&call, 1) - 1
		if int(i) >= len(responses) {
			t.Fatalf("more requests than scripted responses (%d scripted, this is request %d)", len(responses), i+1)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(responses[i]))
	}))
	return server, &call
}

func sseChunk(content, finishReason, toolCallsJSON string) string {
	var extra string
	if toolCallsJSON != "" {
		extra = `,"tool_calls":` + toolCallsJSON
	}
	// The trailing usage-only chunk (empty choices) matches the real
	// server's shape — see client.go's decodeStream, which reads usage
	// from exactly this kind of chunk.
	return `data: {"choices":[{"index":0,"finish_reason":"` + finishReason + `","delta":{"role":"assistant","content":"` + content + `"` + extra + `}}]}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}` + "\n\n" +
		"data: [DONE]\n"
}

func TestRunSubagentTextOnly(t *testing.T) {
	server, _ := scriptedServer(t, []string{
		sseChunk("the answer is 42", "stop", ""),
	})
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	got, usage, err := RunSubagent(context.Background(), t.TempDir(), "minimax-m3", "what is the answer?")
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if got != "the answer is 42" {
		t.Errorf("got %q", got)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		t.Error("expected non-zero usage from the single turn")
	}
}

func TestRunSubagentWithToolCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the secret is banana"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolCall := `[{"index":0,"id":"call_1","type":"function","function":{"name":"view","arguments":"{\"path\":\"notes.txt\"}"}}]`
	server, callCount := scriptedServer(t, []string{
		sseChunk("", "tool_calls", toolCall),
		sseChunk("the secret is banana", "stop", ""),
	})
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	got, usage, err := RunSubagent(context.Background(), dir, "minimax-m3", "find the secret in notes.txt")
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if got != "the secret is banana" {
		t.Errorf("got %q", got)
	}
	if *callCount != 2 {
		t.Errorf("expected 2 requests (one tool round trip), got %d", *callCount)
	}
	// TestRunSubagentUsageAccumulatesAcrossTurns is folded into this
	// test rather than a separate one — it needs the exact same
	// two-turn tool-call fixture. Each of the 2 model requests
	// contributes 100/20 tokens (see sseChunk) — accumulated, not just
	// the last turn's, is the whole point of what this was fixed for.
	if usage.InputTokens != 200 || usage.OutputTokens != 40 {
		t.Errorf("usage = %+v, want 200/40 accumulated across both turns", usage)
	}
}

func TestRunSubagentExceedsMaxTurns(t *testing.T) {
	toolCall := `[{"index":0,"id":"call_1","type":"function","function":{"name":"view","arguments":"{\"path\":\".\"}"}}]`
	responses := make([]string, maxSubagentTurns)
	for i := range responses {
		responses[i] = sseChunk("", "tool_calls", toolCall) // never actually finishes
	}
	server, _ := scriptedServer(t, responses)
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	_, _, err := RunSubagent(context.Background(), t.TempDir(), "minimax-m3", "loop forever")
	if err == nil {
		t.Fatal("expected an error when the subagent never finishes")
	}
	if !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("error = %v", err)
	}
}

func TestDispatchSubagentToolNeedsNoPermission(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "dispatch_subagent", Arguments: `{"task":"find X"}`}}
	if NeedsPermission(call) {
		t.Error("dispatch_subagent needs permission, want auto-allowed (its own tool set is read-only)")
	}
}

func TestSummarizeDispatchSubagent(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "dispatch_subagent", Arguments: `{"task":"find all usages of Foo"}`}}
	if got, want := Summarize(call), "subagent: find all usages of Foo"; got != want {
		t.Errorf("Summarize = %q, want %q", got, want)
	}
}
