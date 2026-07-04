package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleUsageCommandShowsCounts(t *testing.T) {
	m := Model{
		requestCount:      7,
		tokensIn:          45_200,
		tokensOut:         8_100,
		lastContextTokens: 12_400,
	}
	got := m.handleUsageCommand()
	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want a single block", lines)
	}
	joined := lines[0]
	for _, want := range []string{"7", "45.2k", "8.1k", "12.4k"} {
		if !strings.Contains(joined, want) {
			t.Errorf("usage output missing %q: %q", want, joined)
		}
	}
}

// TestUsageCommandDoesNotClaimSessionScope is the regression test for a
// labeling bug: these counters are process-lifetime and were never
// reset or reloaded across /new or /resume, but the output used to say
// "session usage" — misleading now that /sessions and /resume mean one
// process can span several distinct saved conversations.
func TestUsageCommandDoesNotClaimSessionScope(t *testing.T) {
	m := Model{requestCount: 1}
	got := m.handleUsageCommand()
	joined := strings.Join(got.renderedLines(), "\n")
	if strings.Contains(joined, "session usage") {
		t.Errorf("output = %q, want no claim this is scoped to the current session", joined)
	}
	if !strings.Contains(joined, "since launch") {
		t.Errorf("output = %q, want it labeled as process-lifetime (\"since launch\")", joined)
	}
}

// TestUsageCommandDoesNotClaimADollarAmount is the point of this
// feature's scope: OpenCode Go's own "cost" field reads "0" regardless
// of request size (verified against the live API before building
// this), so /usage must not present a fabricated dollar figure.
func TestUsageCommandDoesNotClaimADollarAmount(t *testing.T) {
	m := Model{tokensIn: 1000, tokensOut: 200}
	got := m.handleUsageCommand()
	joined := strings.Join(got.renderedLines(), "\n")
	if strings.Contains(joined, "$0") || strings.Contains(joined, "spent $") {
		t.Errorf("output = %q, want no fabricated dollar amount", joined)
	}
	if !strings.Contains(joined, "doesn't expose real dollar cost") {
		t.Errorf("output = %q, want an explanation of why no dollar estimate is shown", joined)
	}
}

func TestRequestCountIncrementsOnStreamComplete(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, _ := m.handleStreamComplete(agent.Message{Role: "assistant", Content: "done"}, agent.Usage{InputTokens: 100, OutputTokens: 20}, "stop")
	gotModel := got.(Model)
	if gotModel.requestCount != 1 {
		t.Errorf("requestCount = %d, want 1", gotModel.requestCount)
	}
}

func TestRequestCountIncrementsOnSubagentUsageOnly(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"*.go"}`}},
			{ID: "call_2", Function: agent.ToolCallFunction{Name: "dispatch_subagent", Arguments: `{"task":"x"}`}},
		},
	}

	// A plain tool result (no usage) shouldn't count as a request.
	got, _ := m.handleToolResult(agent.ToolResult{ID: "call_1", Content: "no matches"}, false)
	gotModel := got.(Model)
	if gotModel.requestCount != 0 {
		t.Errorf("requestCount = %d, want 0 for a plain tool result", gotModel.requestCount)
	}

	// A subagent result carrying real usage should count as one.
	got, _ = gotModel.handleToolResult(agent.ToolResult{ID: "call_2", Content: "summary", Usage: agent.Usage{InputTokens: 500, OutputTokens: 100}}, false)
	gotModel = got.(Model)
	if gotModel.requestCount != 1 {
		t.Errorf("requestCount = %d, want 1 after a subagent call with real usage", gotModel.requestCount)
	}
}
