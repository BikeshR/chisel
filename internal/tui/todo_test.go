package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestParseTodosValidCall(t *testing.T) {
	call := agent.ToolCall{
		Function: agent.ToolCallFunction{
			Name:      "update_todos",
			Arguments: `{"todos":[{"content":"a","status":"pending"},{"content":"b","status":"in_progress"}]}`,
		},
	}
	todos, ok := parseTodos(call)
	if !ok {
		t.Fatal("expected ok = true")
	}
	if len(todos) != 2 || todos[0].Content != "a" || todos[1].Status != "in_progress" {
		t.Errorf("todos = %+v", todos)
	}
}

func TestParseTodosWrongToolName(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"todos":[]}`}}
	if _, ok := parseTodos(call); ok {
		t.Error("expected ok = false for a non-update_todos call")
	}
}

func TestParseTodosMalformedArguments(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "update_todos", Arguments: `not json`}}
	if _, ok := parseTodos(call); ok {
		t.Error("expected ok = false for malformed arguments")
	}
}

func TestRenderTodosEmptyReturnsEmptyString(t *testing.T) {
	if got := renderTodos(nil); got != "" {
		t.Errorf("renderTodos(nil) = %q, want empty", got)
	}
}

func TestRenderTodosShowsAllStatuses(t *testing.T) {
	todos := []agent.TodoItem{
		{Content: "done thing", Status: "completed"},
		{Content: "current thing", Status: "in_progress"},
		{Content: "future thing", Status: "pending"},
	}
	got := renderTodos(todos)
	for _, want := range []string{"done thing", "current thing", "future thing", "[x]", "[~]", "[ ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderTodos output missing %q: %q", want, got)
		}
	}
}

// TestHandleToolResultExtractsTodos is the integration path: a
// successful update_todos tool result should update Model.todos, read
// from the call's own arguments rather than the result's content
// (which is just a short confirmation string).
func TestHandleToolResultExtractsTodos(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "update_todos", Arguments: `{"todos":[{"content":"step one","status":"in_progress"}]}`}},
		},
	}

	got, _ := m.handleToolResult(agent.ToolResult{ID: "call_1", Content: "todo list updated (1 items)"}, false)
	gotModel := got.(Model)

	if len(gotModel.todos) != 1 || gotModel.todos[0].Content != "step one" {
		t.Errorf("todos = %+v, want the parsed list from the call's arguments", gotModel.todos)
	}
}

func TestHandleToolResultDoesNotExtractTodosOnFailure(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		todos:  []agent.TodoItem{{Content: "existing", Status: "pending"}},
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "update_todos", Arguments: `{"todos":[{"content":"bad","status":"invalid"}]}`}},
		},
	}

	got, _ := m.handleToolResult(agent.ToolResult{ID: "call_1", Content: "invalid status", IsError: true}, false)
	gotModel := got.(Model)

	if len(gotModel.todos) != 1 || gotModel.todos[0].Content != "existing" {
		t.Errorf("todos = %+v, want the previous list preserved on failure", gotModel.todos)
	}
}

func TestRecomputeViewportHeightAccountsForTodos(t *testing.T) {
	m := Model{height: 40}
	m.recomputeViewportHeight()
	withoutTodos := m.viewport.Height

	m.todos = []agent.TodoItem{{Content: "a", Status: "pending"}, {Content: "b", Status: "pending"}}
	m.recomputeViewportHeight()
	withTodos := m.viewport.Height

	if withTodos >= withoutTodos {
		t.Errorf("viewport height with todos (%d) should be smaller than without (%d)", withTodos, withoutTodos)
	}
	if withoutTodos-withTodos != 3 { // 2 todo lines + 1 separator
		t.Errorf("height difference = %d, want 3 (2 items + 1 separator line)", withoutTodos-withTodos)
	}
}
