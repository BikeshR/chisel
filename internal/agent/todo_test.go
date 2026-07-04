package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunUpdateTodosValidInput(t *testing.T) {
	input := json.RawMessage(`{"todos":[{"content":"write tests","status":"in_progress"},{"content":"ship it","status":"pending"}]}`)
	got, err := runUpdateTodos(input)
	if err != nil {
		t.Fatalf("runUpdateTodos: %v", err)
	}
	if !strings.Contains(got, "2") {
		t.Errorf("got %q, want it to mention 2 items", got)
	}
}

func TestRunUpdateTodosRejectsInvalidStatus(t *testing.T) {
	input := json.RawMessage(`{"todos":[{"content":"do a thing","status":"done"}]}`)
	if _, err := runUpdateTodos(input); err == nil {
		t.Error("expected an error for an invalid status value")
	}
}

func TestRunUpdateTodosEmptyList(t *testing.T) {
	input := json.RawMessage(`{"todos":[]}`)
	got, err := runUpdateTodos(input)
	if err != nil {
		t.Fatalf("runUpdateTodos: %v", err)
	}
	if !strings.Contains(got, "0") {
		t.Errorf("got %q, want it to mention 0 items", got)
	}
}

func TestRunUpdateTodosMalformedJSON(t *testing.T) {
	if _, err := runUpdateTodos(json.RawMessage(`not json`)); err == nil {
		t.Error("expected an error for malformed input")
	}
}

func TestUpdateTodosNeedsNoPermission(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "update_todos", Arguments: `{"todos":[]}`}}
	if NeedsPermission(call) {
		t.Error("update_todos needs permission, want auto-allowed — it only affects in-memory UI state")
	}
}

func TestSummarizeUpdateTodos(t *testing.T) {
	call := ToolCall{Function: ToolCallFunction{Name: "update_todos", Arguments: `{"todos":[{"content":"a","status":"pending"},{"content":"b","status":"pending"}]}`}}
	got := Summarize(call)
	if !strings.Contains(got, "2") {
		t.Errorf("Summarize() = %q, want it to mention 2 items", got)
	}
}

func TestExecuteUpdateTodos(t *testing.T) {
	call := ToolCall{ID: "call_1", Function: ToolCallFunction{Name: "update_todos", Arguments: `{"todos":[{"content":"x","status":"pending"}]}`}}
	result := Execute(context.Background(), t.TempDir(), "minimax-m3", call, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
}
