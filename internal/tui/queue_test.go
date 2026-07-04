package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestEnterWhileBusyQueuesInsteadOfSwallowing(t *testing.T) {
	m := newInputModel()
	m.state = stateWaitingModel
	m.textArea.SetValue("what about this too")

	gotTeaModel, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	// A Cmd here is expected now (it persists the queued text to recall
	// history — see recordHistory) — what actually matters is that
	// queueing doesn't start a new model request, checked below via state.
	got := gotTeaModel.(Model)
	if len(got.queuedMessages) != 1 || got.queuedMessages[0] != "what about this too" {
		t.Errorf("queuedMessages = %+v, want the typed text queued", got.queuedMessages)
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want unchanged", got.state)
	}
	if got.textArea.Value() != "" {
		t.Error("textArea should be cleared after queueing")
	}
}

func TestTypingWhileBusyStillEditsTheTextarea(t *testing.T) {
	m := newInputModel()
	m.state = stateExecutingTool

	gotTeaModel, _ := m.handleKey(tea.KeyMsg{Runes: []rune("x"), Type: tea.KeyRunes})
	got := gotTeaModel.(Model)
	if got.textArea.Value() != "x" {
		t.Errorf("textArea value = %q, want %q — typing while busy should still reach the textarea", got.textArea.Value(), "x")
	}
}

func TestEmptyEnterWhileBusyQueuesNothing(t *testing.T) {
	m := newInputModel()
	m.state = stateWaitingModel

	gotTeaModel, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := gotTeaModel.(Model)
	if len(got.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want empty for a blank submission", got.queuedMessages)
	}
}

func TestDequeueOrSubmitDeliversNextQueuedMessage(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"first", "second"}}

	cmd := m.dequeueOrSubmit()
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the queued message's request")
	}
	if len(m.queuedMessages) != 1 || m.queuedMessages[0] != "second" {
		t.Errorf("queuedMessages = %+v, want only \"second\" left", m.queuedMessages)
	}
	if len(m.messages) != 1 || m.messages[0].Content != "first" {
		t.Errorf("messages = %+v, want the first queued message sent", m.messages)
	}
	if m.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", m.state)
	}
}

// TestDequeueOrSubmitRoutesQueuedCommandNotAsPlainText is the regression
// test for a real bug: dequeueOrSubmit used to call submitText directly
// on whatever text was queued, bypassing the "/" and "!" routing submit()
// applies to a live submission — so a "/status" typed while chisel was
// busy got delivered to the model as a literal user message reading
// "/status" instead of ever actually running the command.
func TestDequeueOrSubmitRoutesQueuedCommandNotAsPlainText(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), workDir: "/some/project", queuedMessages: []string{"/status"}}

	m.dequeueOrSubmit()

	if len(m.messages) != 0 {
		t.Errorf("messages = %+v, want /status to never reach the model as a plain message", m.messages)
	}
	found := false
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "workdir") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want /status to have actually run", m.renderedLines())
	}
}

// TestDequeueOrSubmitRoutesQueuedBangNotAsPlainText mirrors the command
// case for "!" bang mode.
func TestDequeueOrSubmitRoutesQueuedBangNotAsPlainText(t *testing.T) {
	m := Model{workDir: t.TempDir(), queuedMessages: []string{"!echo from-queue"}}
	m.bash = agent.NewBashSession(m.workDir)
	defer m.bash.Close()

	cmd := m.dequeueOrSubmit()
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to run the queued bang command")
	}
	if len(m.messages) != 0 {
		t.Errorf("messages = %+v, want bang commands to never reach the model", m.messages)
	}
	if m.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool while the bang command runs", m.state)
	}
}

func TestDequeueOrSubmitNilWhenNothingQueued(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	if cmd := m.dequeueOrSubmit(); cmd != nil {
		t.Error("expected a nil Cmd when nothing is queued")
	}
}

// TestQueuedMessageDeliveredWhenTurnCompletes is an end-to-end check
// through handleStreamComplete: a message queued while busy must
// actually get sent once the in-flight turn finishes, not just sit in
// the queue forever.
func TestQueuedMessageDeliveredWhenTurnCompletes(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"queued one"}}

	got, cmd := m.handleStreamComplete(agent.Message{Role: "assistant", Content: "done"}, agent.Usage{}, "stop")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — the queued message should be sent")
	}
	gotModel := got.(Model)
	if len(gotModel.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want empty after delivery", gotModel.queuedMessages)
	}
	found := false
	for _, msg := range gotModel.messages {
		if msg.Role == "user" && msg.Content == "queued one" {
			found = true
		}
	}
	if !found {
		t.Errorf("messages = %+v, want the queued message appended", gotModel.messages)
	}
	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel — chisel should be busy again with the queued message", gotModel.state)
	}
}

func TestQueueCommandListsQueuedMessages(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"first", "second"}}
	m = m.handleQueueCommand(nil)

	lines := m.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "first") || !strings.Contains(lines[0], "second") {
		t.Errorf("renderedLines() = %+v, want a line listing both queued messages", lines)
	}
}

func TestQueueCommandClearEmptiesTheQueue(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"first", "second"}}
	m = m.handleQueueCommand([]string{"clear"})

	if len(m.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want empty after /queue clear", m.queuedMessages)
	}
}

func TestQueueCommandEmptyReportsEmpty(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	m = m.handleQueueCommand(nil)

	lines := m.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "empty") {
		t.Errorf("renderedLines() = %+v, want a line reporting the queue is empty", lines)
	}
}

func TestStatusLineShowsQueuedCount(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"a", "b"}}
	if !strings.Contains(m.statusLine(200), "2 queued") {
		t.Errorf("statusLine() = %q, want it to mention 2 queued messages", m.statusLine(200))
	}
}

// TestQueuedMessageDeliveredAfterInterrupt confirms a message queued
// while busy still gets delivered even if the turn it was queued during
// ends via interruption (esc) rather than completing normally — esc
// aborts the current response, not the user's other typed plans.
func TestQueuedMessageDeliveredAfterInterrupt(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), queuedMessages: []string{"send me next"}}

	got, cmd := m.handleStreamEvent(streamEventMsg{event: agent.Event{Done: true, Err: context.Canceled}})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — the queued message should be sent even after an interrupt")
	}
	gotModel := got.(Model)
	if len(gotModel.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want delivered", gotModel.queuedMessages)
	}
	found := false
	for _, msg := range gotModel.messages {
		if msg.Content == "send me next" {
			found = true
		}
	}
	if !found {
		t.Error("expected the queued message to have been sent after the interrupt resolved")
	}
}
