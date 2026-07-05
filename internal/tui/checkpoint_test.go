package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/checkpoint"
	"github.com/BikeshR/chisel/internal/session"
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
		sessionID:       "test-session",
		checkpointStore: store,
		state:           stateInput,
	}, workDir
}

func TestRewindListsWhenEmpty(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	got, _ := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no checkpoints yet") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestRewindUnavailableWithoutStore(t *testing.T) {
	m := Model{state: stateInput}
	got, _ := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "aren't available") {
		t.Errorf("lines = %+v, want a not-available message", lines)
	}
}

func TestRewindBlockedMidTurn(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	m.state = stateWaitingModel
	got, _ := m.handleRewindCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "in progress") {
		t.Errorf("lines = %+v, want an in-progress error", lines)
	}
}

func TestDiffUnavailableWithoutStore(t *testing.T) {
	m := Model{state: stateInput}
	got := m.handleDiffCommand()
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "aren't available") {
		t.Errorf("lines = %+v, want a not-available message", lines)
	}
}

func TestDiffNoCheckpointsYet(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	got := m.handleDiffCommand()
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no checkpoints yet") {
		t.Errorf("lines = %+v", lines)
	}
}

// TestDiffShowsChangesSinceLastCheckpoint drives the checkpoint +
// file-edit + /diff flow end-to-end, mirroring how TestRewindFullFlow
// below exercises the equivalent /rewind path.
func TestDiffShowsChangesSinceLastCheckpoint(t *testing.T) {
	m, workDir := newCheckpointTestModel(t)

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("version 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := m.checkpointStore.Checkpoint("first checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	m.checkpoints = append(m.checkpoints, checkpointRecord{hash: hash, label: "first checkpoint"})

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("version 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := m.handleDiffCommand()
	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want a single entry", lines)
	}
	if !strings.Contains(lines[0], "version 1") || !strings.Contains(lines[0], "version 2") {
		t.Errorf("lines[0] = %q, want the actual diff content shown", lines[0])
	}
}

func TestDiffNoChangesSinceLastCheckpoint(t *testing.T) {
	m, workDir := newCheckpointTestModel(t)

	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := m.checkpointStore.Checkpoint("only checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	m.checkpoints = append(m.checkpoints, checkpointRecord{hash: hash, label: "only checkpoint"})

	got := m.handleDiffCommand()
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "no changes since") {
		t.Errorf("lines = %+v, want a no-changes message", lines)
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

	// Stale state from the pre-rewind conversation that must not survive
	// into the rewound one.
	m.lastToolResultIdx = 3
	m.lastToolCallKey = "bash\x00{\"command\":\"rm -rf /tmp/x\"}"
	m.toolCallRepeatCount = 2

	// /rewind 1 should target the checkpoint taken before the most
	// recent (second) turn.
	m, _ = m.handleRewindCommand([]string{"1"})
	if m.pendingRewind == nil || m.pendingRewind.hash != hash2 {
		t.Fatalf("pendingRewind = %+v, want it to target hash2 (%s)", m.pendingRewind, hash2)
	}

	m, cmd := m.handleRewindCommand([]string{"confirm"})
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

	if m.lastToolResultIdx != -1 || m.lastToolCallKey != "" || m.toolCallRepeatCount != 0 {
		t.Errorf("lastToolResultIdx=%d lastToolCallKey=%q toolCallRepeatCount=%d, want all reset after rewind",
			m.lastToolResultIdx, m.lastToolCallKey, m.toolCallRepeatCount)
	}

	// TestRewindPersistsConversationToDisk is the regression test for a
	// real bug: /rewind confirm never returned a Cmd, so the truncated
	// conversation was never saved — quitting right after a rewind
	// resumed the *pre-rewind* conversation against the *rewound* files
	// on next launch.
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to persist the rewound conversation immediately")
	}
	for _, sub := range unpackBatch(t, cmd) {
		if sub == nil {
			continue
		}
		if msg := sub(); msg != nil {
			if _, isGitStatus := msg.(gitStatusMsg); !isGitStatus {
				t.Errorf("sub-cmd() = %v, want nil or a gitStatusMsg", msg)
			}
		}
	}
	resumed, _, ok := session.LoadByID(workDir, m.sessionID)
	if !ok {
		t.Fatal("expected the rewound conversation to be loadable from disk")
	}
	if len(resumed) != 2 || resumed[0].Content != "first turn" {
		t.Errorf("persisted messages = %+v, want the rewound (truncated) conversation, not the pre-rewind one", resumed)
	}
}

func TestRewindConfirmWithoutPendingTarget(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	got, _ := m.handleRewindCommand([]string{"confirm"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing to confirm") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestRewindInvalidIndex(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	m.checkpoints = []checkpointRecord{{hash: "abc", label: "only one"}}

	got, _ := m.handleRewindCommand([]string{"5"})
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

// TestUnrelatedCommandCancelsPendingRewind is the regression test for a
// real bug: the /rewind prompt promises "type /rewind confirm to
// proceed, or anything else to cancel," but only starting a new turn,
// /new, /resume, or /compact actually cleared pendingRewind — an
// intervening /status, /git, a bang command, etc. left it dangling, so
// a stray /rewind confirm minutes later (long after the user's mental
// context had moved on) could still destructively fire.
func TestUnrelatedCommandCancelsPendingRewind(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	target := checkpointRecord{hash: "abc", label: "some checkpoint"}
	m.pendingRewind = &target

	m, _ = m.dispatchText("/status")
	if m.pendingRewind != nil {
		t.Error("an unrelated command should cancel a pending rewind confirmation")
	}
	found := false
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "cancelled") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a notice that the rewind confirmation was cancelled", m.renderedLines())
	}

	// A stray "/rewind confirm" afterward must be a no-op, not
	// destructively fire against the abandoned target.
	m, _ = m.dispatchText("/rewind confirm")
	foundNothingToConfirm := false
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "nothing to confirm") {
			foundNothingToConfirm = true
		}
	}
	if !foundNothingToConfirm {
		t.Errorf("lines = %+v, want \"nothing to confirm\" for a stray /rewind confirm after cancellation", m.renderedLines())
	}
}

// TestRewindFamilyCommandsDoNotSelfCancel confirms /rewind <n> (a
// different target, or the same one) doesn't trigger the "cancelled"
// side effect against itself — re-listing, re-targeting, and confirming
// are all part of the same rewind interaction, not something else
// interrupting it.
func TestRewindFamilyCommandsDoNotSelfCancel(t *testing.T) {
	m, _ := newCheckpointTestModel(t)
	m.checkpoints = []checkpointRecord{{hash: "abc", label: "only one"}}
	target := checkpointRecord{hash: "abc", label: "only one"}
	m.pendingRewind = &target

	m, _ = m.dispatchText("/rewind 1")
	if m.pendingRewind == nil {
		t.Fatal("expected /rewind <n> to still set a pending target, not just cancel the old one")
	}
	for _, l := range m.renderedLines() {
		if strings.Contains(l, "cancelled") {
			t.Errorf("lines = %+v, want no \"cancelled\" notice for a /rewind-family command", m.renderedLines())
		}
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
