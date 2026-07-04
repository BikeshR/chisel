package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

// TestHandleStreamCompleteSurfacesLengthTruncation is the regression
// test for a finding that finish_reason: "length" (the model hit its
// output token limit mid-response) was silently ignored — a truncated
// reply rendered exactly like a normal, complete one, with nothing
// indicating it had been cut off mid-sentence.
func TestHandleStreamCompleteSurfacesLengthTruncation(t *testing.T) {
	m := Model{}
	got, _ := m.handleStreamComplete(agent.Message{Role: "assistant", Content: "cut off mid-sen"}, agent.Usage{}, "length")
	gotModel := got.(Model)

	lines := gotModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a notice mentioning the response was truncated", lines)
	}
}

func TestHandleStreamCompleteNoNoticeOnNormalStop(t *testing.T) {
	m := Model{}
	got, _ := m.handleStreamComplete(agent.Message{Role: "assistant", Content: "all done"}, agent.Usage{}, "stop")
	gotModel := got.(Model)

	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "truncated") {
			t.Errorf("lines = %+v, want no truncation notice for a normal finish_reason", gotModel.renderedLines())
		}
	}
}

// TestHandleToolResultAccountsForSubagentUsage is the regression test
// for a real undercounting bug: dispatch_subagent's own multi-turn
// token spend never reached the status bar's tokensIn/tokensOut totals,
// undercounting exactly when spend was highest (a subagent's own
// research is often the priciest single call in a turn).
func TestHandleToolResultAccountsForSubagentUsage(t *testing.T) {
	m := Model{
		client:      agent.New("minimax-m3"),
		pendingUses: []agent.ToolCall{{ID: "call_1", Function: agent.ToolCallFunction{Name: "dispatch_subagent"}}},
	}

	result := agent.ToolResult{ID: "call_1", Content: "subagent's answer", Usage: agent.Usage{InputTokens: 500, OutputTokens: 80}}
	got, _ := m.handleToolResult(result, false)
	gotModel := got.(Model)

	if gotModel.tokensIn != 500 || gotModel.tokensOut != 80 {
		t.Errorf("tokensIn/tokensOut = %d/%d, want 500/80 from the subagent's usage", gotModel.tokensIn, gotModel.tokensOut)
	}
}
