package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// TestDoomLoopEscalatesRepeatedAutoAllowedCall is the regression test
// for the doom-loop guard: glob is normally auto-allowed (no prompt at
// all), but the same call repeated identically doomLoopThreshold times
// in a row must force a confirmation anyway.
func TestDoomLoopEscalatesRepeatedAutoAllowedCall(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}}

	for i := 0; i < doomLoopThreshold-1; i++ {
		m.pendingUses = []agent.ToolCall{call}
		result, _ := m.dispatchNextTool()
		m = result.(Model)
		if m.state != stateExecutingTool {
			t.Fatalf("iteration %d: state = %v, want stateExecutingTool (should still be auto-allowed before the threshold)", i, m.state)
		}
		// Simulate the call resolving so the next one can dispatch.
		m.pendingUses = nil
	}

	m.pendingUses = []agent.ToolCall{call}
	result, _ := m.dispatchNextTool()
	m = result.(Model)
	if m.state != stateAwaitingPermission {
		t.Fatalf("state = %v, want stateAwaitingPermission once the threshold is hit", m.state)
	}

	found := false
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "loop") && strings.Contains(l, "glob") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a notice mentioning the loop", m.renderedLines())
	}
}

func TestDoomLoopResetsOnDifferentCall(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	globCall := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}}
	grepCall := agent.ToolCall{ID: "call_2", Function: agent.ToolCallFunction{Name: "grep", Arguments: `{"pattern":"foo"}`}}

	for i := 0; i < doomLoopThreshold-1; i++ {
		m.pendingUses = []agent.ToolCall{globCall}
		result, _ := m.dispatchNextTool()
		m = result.(Model)
		m.pendingUses = nil
	}

	// A different call in between should reset the streak.
	m.pendingUses = []agent.ToolCall{grepCall}
	result, _ := m.dispatchNextTool()
	m = result.(Model)
	if m.state != stateExecutingTool {
		t.Fatalf("state = %v, want stateExecutingTool — a different call should reset the repeat count", m.state)
	}
	m.pendingUses = nil

	// Back to glob — should need doomLoopThreshold-1 more repeats
	// again, not immediately trigger.
	m.pendingUses = []agent.ToolCall{globCall}
	result, _ = m.dispatchNextTool()
	m = result.(Model)
	if m.state != stateExecutingTool {
		t.Fatalf("state = %v, want stateExecutingTool — the streak should have reset", m.state)
	}
}

func TestDoomLoopDoesNotOfferAlwaysAllow(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"flaky-check"}`}}

	for i := 0; i < doomLoopThreshold-1; i++ {
		m.pendingUses = []agent.ToolCall{call}
		result, _ := m.dispatchNextTool()
		m = result.(Model)
		// bash normally needs permission every time (not auto-allowed),
		// so these intermediate calls also land in stateAwaitingPermission —
		// resolve each with "y" via the key handler path, mirroring what
		// a real user pressing y would do, before the next dispatch.
		if m.state != stateAwaitingPermission {
			t.Fatalf("iteration %d: state = %v, want stateAwaitingPermission (bash always needs permission)", i, m.state)
		}
		m.pendingUses = nil
	}

	m.entries = nil // isolate the loop-triggering prompt from the earlier iterations' own (legitimate) [y/n/a] prompts
	m.pendingUses = []agent.ToolCall{call}
	result, _ := m.dispatchNextTool()
	m = result.(Model)
	if !m.awaitingLoopConfirmation {
		t.Fatal("expected awaitingLoopConfirmation to be set once the threshold is hit")
	}
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "[y/n/a]") {
			t.Errorf("lines = %+v, want no always-allow option offered while looping", m.renderedLines())
		}
	}
}

func TestDoomLoopIgnoresAlwaysAllowKeypress(t *testing.T) {
	m := Model{
		client:                   agent.New("minimax-m3"),
		state:                    stateAwaitingPermission,
		awaitingLoopConfirmation: true,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"flaky-check"}`}},
		},
	}

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	gotModel := got.(Model)

	if gotModel.sessionAllowlist["bash:flaky-check"] {
		t.Error("pressing 'a' during a doom-loop-forced prompt must not add to the session allowlist")
	}
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool — 'a' should still approve this one call", gotModel.state)
	}
}

func TestDoomLoopDoesNotEscalateAnAlreadyDeniedCall(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetPlanMode(true)
	m := Model{client: client}
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}

	for i := 0; i < doomLoopThreshold+2; i++ {
		m.pendingUses = []agent.ToolCall{call}
		result, _ := m.dispatchNextTool()
		m = result.(Model)
		// A denied call (pendingUses now empty) starts a fresh model
		// request with the denial reason in history — stateWaitingModel,
		// not stateAwaitingPermission. The point of this test is that
		// it's never *escalated into* an ask by the loop guard, however
		// many times it repeats.
		if m.state == stateAwaitingPermission {
			t.Fatalf("iteration %d: state = stateAwaitingPermission — plan mode's deny must never be escalated into an ask", i)
		}
		m.pendingUses = nil
	}
}
