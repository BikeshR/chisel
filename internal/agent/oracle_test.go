package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunConsultOracleUsesPlannerModelWhenSet(t *testing.T) {
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"my advice"}}]}` + "\n\ndata: [DONE]\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	input := json.RawMessage(`{"question":"should I use a mutex or a channel here?"}`)
	_, _, err := runConsultOracle(context.Background(), t.TempDir(), "minimax-m3", "glm-5.2", input)
	if err != nil {
		t.Fatalf("runConsultOracle: %v", err)
	}
	if gotBody.Model != "glm-5.2" {
		t.Errorf("Model = %q, want the planner model used for the oracle consultation", gotBody.Model)
	}
}

func TestRunConsultOracleFallsBackToPrimaryModelWithoutPlannerModel(t *testing.T) {
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"my advice"}}]}` + "\n\ndata: [DONE]\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	input := json.RawMessage(`{"question":"why is this test flaky?"}`)
	_, _, err := runConsultOracle(context.Background(), t.TempDir(), "minimax-m3", "", input)
	if err != nil {
		t.Fatalf("runConsultOracle: %v", err)
	}
	if gotBody.Model != "minimax-m3" {
		t.Errorf("Model = %q, want the primary model when no planner model is configured", gotBody.Model)
	}
}

func TestRunConsultOracleRejectsEmptyQuestion(t *testing.T) {
	if _, _, err := runConsultOracle(context.Background(), t.TempDir(), "minimax-m3", "", json.RawMessage(`{"question":""}`)); err == nil {
		t.Error("expected an error for an empty question")
	}
}

func TestRunConsultOracleMalformedJSON(t *testing.T) {
	if _, _, err := runConsultOracle(context.Background(), t.TempDir(), "minimax-m3", "", json.RawMessage(`not json`)); err == nil {
		t.Error("expected an error for malformed input")
	}
}

func TestOracleNeedsNoPermission(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "consult_oracle", Arguments: `{"question":"x"}`}}
	if NeedsPermission(call) {
		t.Error("consult_oracle needs permission, want auto-allowed — it only ever uses read-only tools")
	}
}

func TestSummarizeConsultOracle(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "consult_oracle", Arguments: `{"question":"is this thread-safe?"}`}}
	got := Summarize(call)
	if !strings.Contains(got, "is this thread-safe?") {
		t.Errorf("Summarize() = %q, want the question shown", got)
	}
}

func TestOracleToolIsInDefaultToolSet(t *testing.T) {
	c := New("minimax-m3")
	found := false
	for _, tool := range c.tools {
		if tool.Function.Name == "consult_oracle" {
			found = true
		}
	}
	if !found {
		t.Error("consult_oracle tool not found in New's default tool set")
	}
}

// TestRunOracleCanReadFilesButNotMutate confirms the oracle's own tool
// set is exactly subagentTools() — it can look at code (glob/grep/view)
// but has no path to a mutating tool, dispatch_subagent, or a further
// consult_oracle call, the same "nothing to gate" guarantee subagents
// rely on.
func TestRunOracleCanReadFilesButNotMutate(t *testing.T) {
	var gotBody chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"done"}}]}` + "\n\ndata: [DONE]\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	_, _, err := RunOracle(context.Background(), t.TempDir(), "minimax-m3", "what's wrong with this code?")
	if err != nil {
		t.Fatalf("RunOracle: %v", err)
	}

	names := map[string]bool{}
	for _, tool := range gotBody.Tools {
		names[tool.Function.Name] = true
	}
	if names["bash"] || names["str_replace_based_edit_tool"] || names["dispatch_subagent"] || names["consult_oracle"] {
		t.Errorf("tools sent = %+v, want only subagentTools() — no mutating tools, no further delegation", gotBody.Tools)
	}
	if !names["glob"] || !names["grep"] || !names["view"] {
		t.Errorf("tools sent = %+v, want glob/grep/view present", gotBody.Tools)
	}
}
