package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/checkpoint"
)

func newCheckpointTestModel(t *testing.T) (Model, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	store, err := checkpoint.Open(workDir)
	if err != nil {
		t.Fatalf("checkpoint.Open: %v", err)
	}
	return Model{
		client:          agent.New("minimax-m3"),
		workDir:         workDir,
		checkpointStore: store,
		state:           stateInput,
	}, workDir
}

func TestRewindListsWhenEmpty(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	got := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no checkpoints yet") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestRewindUnavailableWithoutStore(t *testing.T) {
	m := Model{state: stateInput}
	got := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "aren't available") {
		t.Errorf("lines = %+v, want a not-available message", lines)
	}
}

func TestRewindBlockedMidTurn(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	m.state = stateWaitingModel
	got := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "in progress") {
		t.Errorf("lines = %+v, want an in-progress error", lines)
	}
}

// TestRewindFullFlow drives the whole thing end-to-end: two real turns
// (each writing a different file and checkpointing via submitText),
// then /rewind 1 to target the checkpoint before the second turn,
// /rewind confirm to execute it, and verifies both the file content and
// the conversation were restored.
func TestRewindFullFlow(t *testing.T) {
	m, workDir := newCheckpointTestModel(t)

	// Turn 1: write a.txt, checkpoint synchronously (submitText's Cmd is
	// async in the real TUI, but calling the store directly here keeps
	// the test deterministic without needing to drive tea.Cmd/Msg).
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash1, err := m.checkpointStore.Checkpoint("first turn")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	m.checkpoints = append(m.checkpoints, checkpointRecord{hash: hash1, label: "first turn", messageIndex: len(m.messages)})
	m.messages = append(m.messages, agent.Message{Role: "user", Content: "first turn"})
	m.messages = append(m.messages, agent.Message{Role: "assistant", Content: "done with first turn"})

	// Turn 2: checkpoint happens *before* the turn's changes (matching
	// submitText — a checkpoint captures the state a turn started
	// from), then the file is modified as if a tool call during the
	// turn did it.
	hash2, err := m.checkpointStore.Checkpoint("second turn")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	m.checkpoints = append(m.checkpoints, checkpointRecord{hash: hash2, label: "second turn", messageIndex: len(m.messages)})
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.messages = append(m.messages, agent.Message{Role: "user", Content: "second turn"})
	m.messages = append(m.messages, agent.Message{Role: "assistant", Content: "done with second turn"})

	if len(m.messages) != 4 {
		t.Fatalf("messages = %+v, want 4 before rewinding", m.messages)
	}

	// /rewind 1 should target the checkpoint taken before the most
	// recent (second) turn.
	m = m.handleRewindCommand([]string{"1"})
	if m.pendingRewind == nil || m.pendingRewind.hash != hash2 {
		t.Fatalf("pendingRewind = %+v, want it to target hash2 (%s)", m.pendingRewind, hash2)
	}

	m = m.handleRewindCommand([]string{"confirm"})
	if m.pendingRewind != nil {
		t.Error("pendingRewind should be cleared after confirm")
	}

	got, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Errorf("a.txt = %q, want %q after rewinding past the second turn", got, "v1")
	}

	if len(m.messages) != 2 {
		t.Fatalf("messages = %+v, want 2 (truncated back to before the second turn)", m.messages)
	}
	if m.messages[0].Content != "first turn" {
		t.Errorf("messages[0] = %+v, want the first turn's message preserved", m.messages[0])
	}

	if len(m.checkpoints) != 1 || m.checkpoints[0].hash != hash1 {
		t.Errorf("checkpoints = %+v, want only the first-turn checkpoint remaining", m.checkpoints)
	}
}

func TestRewindConfirmWithoutPendingTarget(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	got := m.handleRewindCommand([]string{"confirm"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing to confirm") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestRewindInvalidIndex(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	m.checkpoints = []checkpointRecord{{hash: "abc", label: "only one"}}

	got := m.handleRewindCommand([]string{"5"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage:") {
		t.Errorf("lines = %+v, want a usage error for an out-of-range index", lines)
	}
	if got.pendingRewind != nil {
		t.Error("pendingRewind should stay nil for an invalid index")
	}
}

func TestNewTurnClearsPendingRewind(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	target := checkpointRecord{hash: "abc"}
	m.pendingRewind = &target

	m, _ = m.submitText("something else entirely")
	if m.pendingRewind != nil {
		t.Error("starting a new turn should clear a pending rewind confirmation")
	}
}

func TestHandleCheckpointCreatedRecordsSuccess(t *testing.T) {
	m := Model{}
	got := m.handleCheckpointCreated(checkpointCreatedMsg{record: checkpointRecord{hash: "abc", label: "test"}})
	if len(got.checkpoints) != 1 || got.checkpoints[0].hash != "abc" {
		t.Errorf("checkpoints = %+v", got.checkpoints)
	}
}

func TestHandleCheckpointCreatedReportsFailureQuietly(t *testing.T) {
	m := Model{}
	got := m.handleCheckpointCreated(checkpointCreatedMsg{err: os.ErrPermission})
	if len(got.checkpoints) != 0 {
		t.Errorf("checkpoints = %+v, want none recorded on failure", got.checkpoints)
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "checkpoint failed") {
		t.Errorf("lines = %+v", lines)
	}
}
