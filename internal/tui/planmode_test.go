package tui

import (
	"os"
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

func TestHandleAcceptEditsCommandToggle(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}

	got := m.handleAcceptEditsCommand()
	if got.client.Mode() != agent.ModeAcceptEdits {
		t.Errorf("Mode() = %v after first /accept-edits, want ModeAcceptEdits", got.client.Mode())
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "on") {
		t.Errorf("lines = %+v, want a line announcing accept-edits mode is on", lines)
	}

	got = got.handleAcceptEditsCommand()
	if got.client.Mode() != agent.ModeNormal {
		t.Errorf("Mode() = %v after second /accept-edits, want ModeNormal", got.client.Mode())
	}
}

// TestPlanAndAcceptEditsAreMutuallyExclusive confirms the two commands
// operate on one shared underlying mode, not two independent flags —
// switching into one always wins over the other, matching how
// decidePermission treats them as mutually exclusive states.
func TestPlanAndAcceptEditsAreMutuallyExclusive(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}

	m = m.handleAcceptEditsCommand()
	if m.client.Mode() != agent.ModeAcceptEdits {
		t.Fatalf("Mode() = %v, want ModeAcceptEdits", m.client.Mode())
	}

	m = m.handlePlanCommand()
	if m.client.Mode() != agent.ModePlan {
		t.Errorf("Mode() = %v, want ModePlan — /plan must win over an active accept-edits mode", m.client.Mode())
	}

	m = m.handleAcceptEditsCommand()
	if m.client.Mode() != agent.ModeAcceptEdits {
		t.Errorf("Mode() = %v, want ModeAcceptEdits — /accept-edits must win back over an active plan mode", m.client.Mode())
	}
}

// TestDispatchNextToolAllowsFileEditInAcceptEditsMode is the end-to-end
// version of TestDecidePermissionAcceptEditsAllowsFileEdit, through the
// real dispatch path a live turn actually takes.
func TestDispatchNextToolAllowsFileEditInAcceptEditsMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.go", []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := agent.New("minimax-m3")
	client.SetMode(agent.ModeAcceptEdits)

	m := Model{
		client:  client,
		workDir: dir,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{
				Name:      "str_replace_based_edit_tool",
				Arguments: `{"command":"str_replace","path":"a.go","old_str":"package main","new_str":"package other"}`,
			}},
		},
	}

	got, cmd := m.dispatchNextTool()
	gotModel := got.(Model)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — the edit should run, not wait on a permission prompt")
	}
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool — accept-edits must not stop at a permission prompt", gotModel.state)
	}
}

// TestDispatchNextToolStillAsksForBashInAcceptEditsMode confirms the
// permission-prompt path (not just decidePermission in isolation) is
// unaffected for bash while accept-edits is on.
func TestDispatchNextToolStillAsksForBashInAcceptEditsMode(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetMode(agent.ModeAcceptEdits)

	m := Model{
		client: client,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}},
		},
	}

	got, _ := m.dispatchNextTool()
	gotModel := got.(Model)
	if gotModel.state != stateAwaitingPermission {
		t.Errorf("state = %v, want stateAwaitingPermission — bash must still ask even in accept-edits mode", gotModel.state)
	}
}

func TestStatusLineShowsAcceptEditsMode(t *testing.T) {
	client := agent.New("minimax-m3")
	client.SetMode(agent.ModeAcceptEdits)
	m := Model{client: client}
	if !strings.Contains(m.statusLine(200), "ACCEPT EDITS") {
		t.Error("status line doesn't show ACCEPT EDITS with accept-edits mode on")
	}
}
