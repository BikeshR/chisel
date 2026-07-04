package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/session"
)

func TestHandleSessionsCommandListsAndMarksCurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"

	oldID := "20260101T000000.000000000Z"
	if err := session.Save(workDir, oldID, []agent.Message{{Role: "user", Content: "fix the login bug"}}); err != nil {
		t.Fatal(err)
	}
	currentID := "20260601T000000.000000000Z"
	if err := session.Save(workDir, currentID, []agent.Message{{Role: "user", Content: "add dark mode"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{workDir: workDir, sessionID: currentID}
	got := m.handleSessionsCommand()

	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (a single block)", len(lines))
	}
	text := lines[0]
	if !strings.Contains(text, "add dark mode") || !strings.Contains(text, "fix the login bug") {
		t.Errorf("listing = %q, want both session titles", text)
	}
	if !strings.Contains(text, "(current)") {
		t.Errorf("listing = %q, want the current session marked", text)
	}
}

func TestHandleSessionsCommandEmptyDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := Model{workDir: "/home/brana/code/testproj"}
	got := m.handleSessionsCommand()

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no saved sessions") {
		t.Errorf("lines = %+v, want a single line reporting no saved sessions", lines)
	}
}

func TestHandleResumeCommandSwitchesSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"

	oldID := "20260101T000000.000000000Z"
	if err := session.Save(workDir, oldID, []agent.Message{{Role: "user", Content: "old conversation"}}); err != nil {
		t.Fatal(err)
	}
	currentID := "20260601T000000.000000000Z"
	if err := session.Save(workDir, currentID, []agent.Message{{Role: "user", Content: "current conversation"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{
		workDir:             workDir,
		sessionID:           currentID,
		messages:            []agent.Message{{Role: "user", Content: "current conversation"}},
		todos:               []agent.TodoItem{{Content: "stale todo", Status: "in_progress"}},
		checkpoints:         []checkpointRecord{{hash: "deadbeef", label: "stale", messageIndex: 1}},
		pendingRewind:       &checkpointRecord{hash: "deadbeef"},
		lastToolResultIdx:   3,
		lastToolCallKey:     "bash\x00{\"command\":\"ls\"}",
		toolCallRepeatCount: 2,
	}

	// The older session is second in /sessions' most-recent-first
	// listing (index 2).
	got, cmd := m.handleResumeCommand([]string{"2"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to persist the resumed session's bumped SavedAt immediately")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("cmd() = %v, want nil (no save error)", msg)
	}

	if got.sessionID != oldID {
		t.Errorf("sessionID = %q, want %q", got.sessionID, oldID)
	}
	if len(got.messages) != 1 || got.messages[0].Content != "old conversation" {
		t.Errorf("messages = %+v, want the old conversation loaded", got.messages)
	}
	if len(got.todos) != 0 {
		t.Errorf("todos = %+v, want cleared after switching sessions", got.todos)
	}
	if len(got.checkpoints) != 0 {
		t.Errorf("checkpoints = %+v, want cleared after switching sessions", got.checkpoints)
	}
	if got.pendingRewind != nil {
		t.Error("expected pendingRewind cleared after switching sessions")
	}
	if got.lastToolResultIdx != -1 || got.lastToolCallKey != "" || got.toolCallRepeatCount != 0 {
		t.Errorf("lastToolResultIdx=%d lastToolCallKey=%q toolCallRepeatCount=%d, want all reset by /resume",
			got.lastToolResultIdx, got.lastToolCallKey, got.toolCallRepeatCount)
	}

	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "resumed") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line confirming the resume", lines)
	}

	// LoadLatest must now resolve to the resumed session, not whichever
	// one happened to be saved most recently before the switch.
	_, _, resumedID, ok, corrupt := session.LoadLatest(workDir)
	if !ok || corrupt {
		t.Fatalf("LoadLatest after /resume: ok=%v corrupt=%v", ok, corrupt)
	}
	if resumedID != oldID {
		t.Errorf("LoadLatest resumed id = %q, want %q", resumedID, oldID)
	}
}

func TestHandleResumeCommandInvalidIndex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"
	if err := session.Save(workDir, session.NewID(), []agent.Message{{Role: "user", Content: "only session"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{workDir: workDir}
	got, _ := m.handleResumeCommand([]string{"99"})

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v, want a usage error for an out-of-range index", lines)
	}
}

func TestHandleResumeCommandRestrictedToStateInput(t *testing.T) {
	m := Model{workDir: t.TempDir(), state: stateWaitingModel}
	got, _ := m.handleResumeCommand([]string{"1"})

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "turn is in progress") {
		t.Errorf("lines = %+v, want a refusal while a turn is in progress", lines)
	}
}

func TestHandleResumeCommandNoArgsListsLikeSessionsCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"
	if err := session.Save(workDir, session.NewID(), []agent.Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{workDir: workDir}
	got, _ := m.handleResumeCommand(nil)

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "hello") {
		t.Errorf("lines = %+v, want the session listing", lines)
	}
}
