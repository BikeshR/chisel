package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestRenderHistory(t *testing.T) {
	messages := []agent.Message{
		{Role: "user", Content: "list go files"},
		{
			Role: "assistant",
			ToolCalls: []agent.ToolCall{
				{ID: "call_1", Type: "function", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "main.go\nbash.go"},
		{Role: "assistant", Content: "Found 2 files."},
	}

	lines := renderHistory(messages, false)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %+v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "list go files") {
		t.Errorf("line 0 = %q, want the user text", lines[0])
	}
	if !strings.Contains(lines[1], "glob") {
		t.Errorf("line 1 = %q, want it to mention the glob tool", lines[1])
	}
	if !strings.Contains(lines[2], "✓") || !strings.Contains(lines[2], "main.go") {
		t.Errorf("line 2 = %q, want a success marker and the tool output", lines[2])
	}
	if !strings.Contains(lines[3], "Found 2 files") {
		t.Errorf("line 3 = %q, want the assistant's final text", lines[3])
	}
}

func TestRenderHistoryErrorToolResult(t *testing.T) {
	messages := []agent.Message{
		{Role: "tool", ToolCallID: "call_1", Content: "Error: permission denied"},
	}
	lines := renderHistory(messages, false)
	if len(lines) != 1 || !strings.Contains(lines[0], "✗") || !strings.Contains(lines[0], "permission denied") {
		t.Errorf("lines = %+v, want a single failure line without the raw \"Error: \" prefix", lines)
	}
	if strings.Contains(lines[0], "Error: ") {
		t.Errorf("lines[0] = %q, want the \"Error: \" wire prefix stripped", lines[0])
	}
}

func TestRenderHistorySkipsEmptyAssistantContent(t *testing.T) {
	// An assistant turn that was pure tool-calling (no text) shouldn't
	// produce a blank "chisel  " line.
	messages := []agent.Message{
		{Role: "assistant", ToolCalls: []agent.ToolCall{
			{ID: "call_1", Type: "function", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}},
		}},
	}
	lines := renderHistory(messages, false)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want exactly 1 (the tool-call summary): %+v", len(lines), lines)
	}
}

func TestHumanizeSince(t *testing.T) {
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5 min ago"},
		{3 * time.Hour, "3 hr ago"},
		{50 * time.Hour, "2 days ago"},
	}
	for _, c := range cases {
		got := humanizeSince(time.Now().Add(-c.ago))
		if got != c.want {
			t.Errorf("humanizeSince(%s ago) = %q, want %q", c.ago, got, c.want)
		}
	}
}
