package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agentmemory"
)

func TestRunRememberValidInput(t *testing.T) {
	dir := t.TempDir()
	input := json.RawMessage(`{"note":"this repo uses tabs not spaces"}`)
	got, err := runRemember(dir, input)
	if err != nil {
		t.Fatalf("runRemember: %v", err)
	}
	if got == "" {
		t.Error("expected a non-empty confirmation")
	}

	content, found := agentmemory.Load(dir)
	if !found || !strings.Contains(content, "this repo uses tabs not spaces") {
		t.Errorf("agentmemory.Load = %q, %v, want the note persisted", content, found)
	}
}

func TestRunRememberRejectsEmptyNote(t *testing.T) {
	if _, err := runRemember(t.TempDir(), json.RawMessage(`{"note":""}`)); err == nil {
		t.Error("expected an error for an empty note")
	}
}

func TestRunRememberMalformedJSON(t *testing.T) {
	if _, err := runRemember(t.TempDir(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected an error for malformed input")
	}
}

func TestRememberNeedsNoPermission(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "remember", Arguments: `{"note":"x"}`}}
	if NeedsPermission(call) {
		t.Error("remember needs permission, want auto-allowed — it only ever touches chisel's own .chisel/MEMORY.md, not project files")
	}
}

func TestSummarizeRemember(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "remember", Arguments: `{"note":"use gofmt before committing"}`}}
	got := Summarize(call)
	if !strings.Contains(got, "use gofmt before committing") {
		t.Errorf("Summarize() = %q, want the note shown", got)
	}
}

func TestExecuteRemember(t *testing.T) {
	dir := t.TempDir()
	call := ToolCall{ID: "call_1", Function: ToolCallFunction{Name: "remember", Arguments: `{"note":"a durable fact"}`}}
	result := Execute(context.Background(), dir, "minimax-m3", call, nil, nil, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if content, found := agentmemory.Load(dir); !found || !strings.Contains(content, "a durable fact") {
		t.Errorf("agentmemory.Load = %q, %v, want the note persisted via Execute", content, found)
	}
}

// TestRememberToolIsInDefaultToolSet confirms buildTools actually
// registers it — a schema that exists but was never wired into the
// tool set the model is offered would be silently useless.
func TestRememberToolIsInDefaultToolSet(t *testing.T) {
	c := New("minimax-m3")
	found := false
	for _, tool := range c.tools {
		if tool.Function.Name == "remember" {
			found = true
		}
	}
	if !found {
		t.Error("remember tool not found in New's default tool set")
	}
}

// TestSetAgentMemoryAugmentsSystemPrompt verifies the actual request
// body, the same way TestPlanModeAugmentsSystemPrompt does for plan
// mode — the field is only useful insofar as it changes what the model
// is actually sent.
func TestSetAgentMemoryAugmentsSystemPrompt(t *testing.T) {
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	c := New("minimax-m3")
	c.SetAgentMemory("- this repo uses tabs not spaces")
	ch, err := c.SendStreaming(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	for range ch {
	}

	if !strings.Contains(gotBody.Messages[0].Content, "this repo uses tabs not spaces") {
		t.Errorf("system prompt = %q, want it to include the agent memory content", gotBody.Messages[0].Content)
	}
}
