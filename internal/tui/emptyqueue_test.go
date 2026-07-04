package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDispatchNextToolEmptyQueueDoesNotPanic(t *testing.T) {
	m := Model{state: stateAwaitingPermission}
	got, cmd := m.dispatchNextTool()
	gotModel := got.(Model)
	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput", gotModel.state)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd for an empty queue")
	}
}

func TestHandleKeyAwaitingPermissionEmptyQueueDoesNotPanic(t *testing.T) {
	m := Model{state: stateAwaitingPermission}
	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)
	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput", gotModel.state)
	}
}
