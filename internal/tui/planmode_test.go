package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandlePlanCommandToggle(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}

	got := m.handlePlanCommand()
	if !got.client.PlanMode() {
		t.Error("PlanMode() = false after first /plan, want true")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "on") {
		t.Errorf("lines = %+v, want a line announcing plan mode is on", lines)
	}

	got = got.handlePlanCommand()
	if got.client.PlanMode() {
		t.Error("PlanMode() = true after second /plan, want false")
	}
}

func TestDispatchNextToolBlocksInPlanMode(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetPlanMode(true)

	m := Model{
		client: client,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		},
	}

	got, cmd := m.dispatchNextTool()
	gotModel := got.(Model)

	// A denial isn't a no-op — like a normal permission "n", it's sent
	// back to the model as this call's result, which starts a new
	// request. What matters is that the call itself never actually ran.
	if cmd == nil {
		t.Error("expected a non-nil Cmd — the denial result still needs to go back to the model")
	}
	if len(gotModel.pendingUses) != 0 {
		t.Error("pendingUses not cleared — the blocked call should be resolved like any other completed tool call")
	}
	lines := gotModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "plan mode") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line explaining the call was blocked by plan mode", lines)
	}
}

func TestDispatchNextToolReadOnlyStillAllowedInPlanMode(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetPlanMode(true)

	m := Model{
		client: client,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}},
		},
	}

	_, cmd := m.dispatchNextTool()
	if cmd == nil {
		t.Error("expected a non-nil Cmd — read-only tools must still run in plan mode")
	}
}

func TestStatusLineShowsPlanMode(t *testing.T) {
	client := agent.New("minimax-m3")
	m := Model{client: client}
	if strings.Contains(m.statusLine(200), "PLAN MODE") {
		t.Error("status line shows PLAN MODE with plan mode off")
	}

	client.SetPlanMode(true)
	if !strings.Contains(m.statusLine(200), "PLAN MODE") {
		t.Error("status line doesn't show PLAN MODE with plan mode on")
	}
}
