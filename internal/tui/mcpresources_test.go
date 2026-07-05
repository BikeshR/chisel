package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/mcp"
)

func TestHandleMCPPromptsCommandEmpty(t *testing.T) {
	m := Model{mcp: &mcp.Registry{}}
	got := m.handleMCPPromptsCommand()
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no MCP prompts available") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleMCPResourcesCommandEmpty(t *testing.T) {
	m := Model{mcp: &mcp.Registry{}}
	got := m.handleMCPResourcesCommand()
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no MCP resources available") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleMCPPromptCommandUsage(t *testing.T) {
	m := Model{mcp: &mcp.Registry{}}
	got, cmd := m.handleMCPPromptCommand(nil)
	if cmd != nil {
		t.Error("expected a nil Cmd for a missing-arguments usage error")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleMCPPromptCommandRejectsMalformedArgument(t *testing.T) {
	m := Model{mcp: &mcp.Registry{}}
	got, cmd := m.handleMCPPromptCommand([]string{"server", "name", "not-a-key-value-pair"})
	if cmd != nil {
		t.Error("expected a nil Cmd for a malformed argument")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "key=value") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleMCPPromptCommandStartsAsyncFetch(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), mcp: &mcp.Registry{}}
	got, cmd := m.handleMCPPromptCommand([]string{"server", "review", "focus=security"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to fetch the prompt")
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel while the fetch is pending", got.state)
	}
}

func TestHandleMCPResourceCommandUsage(t *testing.T) {
	m := Model{mcp: &mcp.Registry{}}
	got, cmd := m.handleMCPResourceCommand(nil)
	if cmd != nil {
		t.Error("expected a nil Cmd for a missing-arguments usage error")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleMCPResourceCommandStartsAsyncFetch(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), mcp: &mcp.Registry{}}
	got, cmd := m.handleMCPResourceCommand([]string{"server", "file:///notes.txt"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to fetch the resource")
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel while the fetch is pending", got.state)
	}
}

func TestHandleMCPPromptResultSubmitsFetchedText(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, cmd := m.handleMCPPromptResult(mcpPromptResultMsg{text: "review this code for bugs"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the request")
	}
	if len(got.messages) != 1 || got.messages[0].Content != "review this code for bugs" {
		t.Errorf("messages = %+v, want the fetched prompt text submitted", got.messages)
	}
}

func TestHandleMCPPromptResultReportsError(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, _ := m.handleMCPPromptResult(mcpPromptResultMsg{err: errHookBroken})
	if got.state != stateInput {
		t.Errorf("state = %v, want stateInput after an error", got.state)
	}
	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want nothing submitted on error", got.messages)
	}
	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, errHookBroken.Error()) {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the error surfaced", lines)
	}
}

func TestHandleMCPResourceResultSubmitsFramedContent(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, cmd := m.handleMCPResourceResult(mcpResourceResultMsg{server: "myserver", uri: "file:///notes.txt", content: "the notes"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the request")
	}
	if len(got.messages) != 1 {
		t.Fatalf("messages = %+v, want exactly one submitted", got.messages)
	}
	content := got.messages[0].Content
	if !strings.Contains(content, "myserver") || !strings.Contains(content, "file:///notes.txt") || !strings.Contains(content, "the notes") {
		t.Errorf("submitted content = %q, want the server, uri, and resource content all present", content)
	}
}

func TestHandleMCPResourceResultReportsError(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, _ := m.handleMCPResourceResult(mcpResourceResultMsg{err: errHookBroken})
	if got.state != stateInput {
		t.Errorf("state = %v, want stateInput after an error", got.state)
	}
	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want nothing submitted on error", got.messages)
	}
}

// TestMCPPromptCommandRoutesThroughHandleCommand confirms /mcp-prompt
// is actually reachable through the normal command dispatch, not just
// its handler in isolation.
func TestMCPPromptCommandRoutesThroughHandleCommand(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), mcp: &mcp.Registry{}}
	got, cmd := m.handleCommand("/mcp-prompt server review")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	gotModel := got
	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", gotModel.state)
	}
}
