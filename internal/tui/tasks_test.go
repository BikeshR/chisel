package tui

import (
	"context"
	"strings"
	"testing"
)

func TestHandleTasksCommandEmpty(t *testing.T) {
	m := Model{}
	got := m.handleTasksCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no background tasks") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleTasksCommandListsRunningAndFinished(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	m := Model{backgroundTasks: map[string]*backgroundTask{
		"bg_1": {command: "sleep 30", cancel: cancel, running: true},
		"bg_2": {command: "echo done", cancel: cancel, running: false},
	}}
	got := m.handleTasksCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want a single entry", lines)
	}
	text := lines[0]
	for _, want := range []string{"bg_1", "sleep 30", "running", "bg_2", "echo done", "finished"} {
		if !strings.Contains(text, want) {
			t.Errorf("tasks output missing %q: %q", want, text)
		}
	}
}

func TestHandleTasksCommandCancelUnknownID(t *testing.T) {
	m := Model{}
	got := m.handleTasksCommand([]string{"cancel", "bg_999"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no background task") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleTasksCommandCancelAlreadyFinished(t *testing.T) {
	m := Model{backgroundTasks: map[string]*backgroundTask{
		"bg_1": {command: "echo done", running: false},
	}}
	got := m.handleTasksCommand([]string{"cancel", "bg_1"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "already finished") {
		t.Errorf("lines = %+v", lines)
	}
}

// TestHandleTasksCommandCancelRunningCallsCancelFunc is the direct
// test that /tasks cancel actually fires the task's own CancelFunc —
// the same one CancelBackgroundTasks uses at exit, just scoped to one
// task instead of all of them.
func TestHandleTasksCommandCancelRunningCallsCancelFunc(t *testing.T) {
	called := false
	m := Model{backgroundTasks: map[string]*backgroundTask{
		"bg_1": {command: "sleep 30", cancel: func() { called = true }, running: true},
	}}
	got := m.handleTasksCommand([]string{"cancel", "bg_1"})

	if !called {
		t.Error("expected the task's CancelFunc to be called")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "cancelling") {
		t.Errorf("lines = %+v, want a line confirming cancellation started", lines)
	}
}

func TestHandleTasksCommandUsageForUnknownArg(t *testing.T) {
	m := Model{}
	got := m.handleTasksCommand([]string{"bogus"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleTasksCommandCancelMissingID(t *testing.T) {
	m := Model{}
	got := m.handleTasksCommand([]string{"cancel"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v", lines)
	}
}
