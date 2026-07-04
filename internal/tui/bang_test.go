package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestRunBangEmptyCommand(t *testing.T) {
	m := newInputModel()
	got, cmd := m.runBang("   ")
	if cmd != nil {
		t.Error("expected a nil Cmd for an empty command")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "needs a command") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestRunBangEchoesCommandAndDispatchesAsync(t *testing.T) {
	m := newInputModel()
	m.bash = agent.NewBashSession(t.TempDir())
	defer m.bash.Close()

	got, cmd := m.runBang("echo hello")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	if got.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool while the command runs", got.state)
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "! echo hello") {
		t.Errorf("lines = %+v, want the command echoed", lines)
	}
	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want bang commands to never reach the model", got.messages)
	}
}

func TestBangFullFlowRunsThroughPersistentBashSession(t *testing.T) {
	m := newInputModel()
	m.bash = agent.NewBashSession(t.TempDir())
	defer m.bash.Close()

	got, cmd := m.runBang("echo hello-from-bang")
	msg := cmd()
	result, ok := msg.(bangResultMsg)
	if !ok {
		t.Fatalf("expected bangResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if !strings.Contains(result.output, "hello-from-bang") {
		t.Errorf("output = %q", result.output)
	}

	finalModel, followUp := got.handleBangResult(result)
	if finalModel.state != stateInput {
		t.Errorf("state = %v, want stateInput after the command finishes", finalModel.state)
	}
	lines := finalModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "hello-from-bang") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the command's output rendered", lines)
	}

	// A bang command can change git state just as easily as a tool call
	// can — handleBangResult must refresh the cached status-bar segment
	// too, not just once the model's own turn ends. tea.Batch collapses
	// to the single Cmd directly (not a BatchMsg) when it's the only
	// non-nil one, which is exactly this case (nothing queued, nothing
	// buffered) — so check for either shape.
	if followUp == nil {
		t.Fatal("expected a non-nil Cmd (refreshGitStatus, even with nothing queued)")
	}
	switch msg := followUp().(type) {
	case gitStatusMsg:
		// The lone survivor after tea.Batch's nil-filtering — expected.
	case tea.BatchMsg:
		found := false
		for _, sub := range msg {
			if sub == nil {
				continue
			}
			if _, ok := sub().(gitStatusMsg); ok {
				found = true
			}
		}
		if !found {
			t.Error("expected refreshGitStatus's Cmd among handleBangResult's batch")
		}
	default:
		t.Errorf("followUp() = %T, want gitStatusMsg or a tea.BatchMsg containing one", msg)
	}
}

// TestBangSharesStateWithPersistentBashSession is the reason bang mode
// dispatches through m.bash rather than a one-off subprocess: cd and
// exported env vars set by one should be visible to the other, the
// same way a real shell's forward slashes work.
func TestBangSharesStateWithPersistentBashSession(t *testing.T) {
	dir := t.TempDir()
	bash := agent.NewBashSession(dir)
	defer bash.Close()

	if _, err := bash.Run(context.Background(), "export BANG_TEST_VAR=from-bash-tool", false); err != nil {
		t.Fatal(err)
	}

	m := newInputModel()
	m.bash = bash
	_, cmd := m.runBang("echo $BANG_TEST_VAR")
	result := cmd().(bangResultMsg)
	if strings.TrimSpace(result.output) != "from-bash-tool" {
		t.Errorf("output = %q, want the env var set via the bash tool to be visible to bang mode", result.output)
	}
}

func TestRunBangNoSessionAvailable(t *testing.T) {
	m := newInputModel()
	_, cmd := m.runBang("echo hi")
	msg := cmd()
	result, ok := msg.(bangResultMsg)
	if !ok {
		t.Fatalf("expected bangResultMsg, got %T", msg)
	}
	if result.err == nil {
		t.Error("expected an error when no bash session is configured")
	}
}

func TestSubmitDispatchesBangMode(t *testing.T) {
	m := newInputModel()
	m.bash = agent.NewBashSession(t.TempDir())
	defer m.bash.Close()
	m.textArea.SetValue("!echo from-submit")

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool", gotModel.state)
	}
	if len(gotModel.messages) != 0 {
		t.Errorf("messages = %+v, want bang mode to never touch the conversation", gotModel.messages)
	}
}
