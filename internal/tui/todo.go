package tui

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/BikeshR/chisel/internal/agent"
)

// parseTodos extracts the todo list from an update_todos call's own
// arguments — the tool's ToolResult content sent back to the model is
// just a short confirmation string (see agent.runUpdateTodos), not the
// structured list itself, so the TUI reads the call's arguments
// directly to render it live. ok is false for anything other than a
// well-formed update_todos call.
func parseTodos(call agent.ToolCall) (todos []agent.TodoItem, ok bool) {
	if call.Function.Name != "update_todos" {
		return nil, false
	}
	var in struct {
		Todos []agent.TodoItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil {
		return nil, false
	}
	return in.Todos, true
}

var (
	// Plain dim color, not Strikethrough — lipgloss renders a
	// strikethrough style by wrapping *each rune* in its own escape
	// codes rather than the whole string once, which is harmless in a
	// real terminal but needlessly verbose output for what a flat color
	// change already conveys clearly enough.
	todoDoneStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	todoInProgressStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("223")).Bold(true)
)

// renderTodos renders the current checklist as a small persistent
// block — one line per item, a distinct marker per status. Returns ""
// when there's nothing to show, so callers can skip it entirely rather
// than rendering an empty box.
func renderTodos(todos []agent.TodoItem) string {
	if len(todos) == 0 {
		return ""
	}
	lines := make([]string, len(todos))
	for i, item := range todos {
		switch item.Status {
		case "completed":
			lines[i] = todoDoneStyle.Render("[x] " + item.Content)
		case "in_progress":
			lines[i] = todoInProgressStyle.Render("[~] " + item.Content)
		default:
			lines[i] = dimStyle.Render("[ ] " + item.Content)
		}
	}
	return strings.Join(lines, "\n")
}
