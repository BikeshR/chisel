package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/session"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestHandleModelCheckResult(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		m := Model{state: stateWaitingModel}
		got, cmd := m.handleModelCheckResult(modelCheckResultMsg{model: "minimax-m3", reply: "ok"})
		gotModel := got.(Model)

		if gotModel.state != stateInput {
			t.Errorf("state = %v, want stateInput", gotModel.state)
		}
		if cmd != nil {
			t.Error("expected a nil Cmd after a check result")
		}
		lines := gotModel.renderedLines()
		if len(lines) != 1 || !strings.Contains(lines[0], "minimax-m3") || !strings.Contains(lines[0], "ok") {
			t.Errorf("lines = %+v, want a line mentioning the model and its reply", lines)
		}
	})

	t.Run("failure", func(t *testing.T) {
		m := Model{state: stateWaitingModel}
		got, _ := m.handleModelCheckResult(modelCheckResultMsg{model: "kimi-k2.6", err: errors.New("upstream request failed")})
		gotModel := got.(Model)

		if gotModel.state != stateInput {
			t.Errorf("state = %v, want stateInput", gotModel.state)
		}
		lines := gotModel.renderedLines()
		if len(lines) != 1 || !strings.Contains(lines[0], "kimi-k2.6") || !strings.Contains(lines[0], "upstream request failed") {
			t.Errorf("lines = %+v, want a line mentioning the model and the error", lines)
		}
	})
}

// TestHandleModelCheckResultDeliversQueuedMessage is the regression test
// for the same class of bug as compact_test.go's version: a message
// typed while a /model check was running used to be queued and then
// left stranded, since handleModelCheckResult never called
// dequeueOrSubmit on returning to stateInput.
func TestHandleModelCheckResultDeliversQueuedMessage(t *testing.T) {
	m := Model{
		client:         agent.New("minimax-m3"),
		state:          stateWaitingModel,
		queuedMessages: []string{"go ahead"},
	}
	got, cmd := m.handleModelCheckResult(modelCheckResultMsg{model: "minimax-m3", reply: "ok"})
	gotModel := got.(Model)

	if len(gotModel.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want the queued message delivered", gotModel.queuedMessages)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to deliver the queued message")
	}
	if len(gotModel.messages) != 1 || gotModel.messages[0].Content != "go ahead" {
		t.Errorf("messages = %+v, want the queued message sent as the next turn", gotModel.messages)
	}
}

func TestHandleNewCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"
	oldID := session.NewID()

	if err := session.Save(workDir, oldID, []agent.Message{{Role: "user", Content: "old conversation"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{
		workDir:             workDir,
		sessionID:           oldID,
		messages:            []agent.Message{{Role: "user", Content: "old conversation"}},
		entries:             []entry{{styled: "you  old conversation"}},
		lastToolResultIdx:   3,
		lastToolCallKey:     "bash\x00{\"command\":\"ls\"}",
		toolCallRepeatCount: 2,
	}
	got, cmd := m.handleNewCommand()

	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want empty", got.messages)
	}
	if got.sessionID == oldID {
		t.Error("expected /new to mint a fresh session id, not reuse the old one")
	}
	if got.lastToolResultIdx != -1 || got.lastToolCallKey != "" || got.toolCallRepeatCount != 0 {
		t.Errorf("lastToolResultIdx=%d lastToolCallKey=%q toolCallRepeatCount=%d, want all reset by /new",
			got.lastToolResultIdx, got.lastToolCallKey, got.toolCallRepeatCount)
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "new session") {
		t.Errorf("lines = %+v, want a single line announcing a new session", lines)
	}

	// The old session must NOT be deleted — /new starts fresh, it
	// doesn't destroy history; the previous conversation stays resumable.
	if _, _, ok := session.LoadByID(workDir, oldID); !ok {
		t.Error("expected the previous session to still be loadable after /new")
	}

	// /new must save immediately (even with zero messages) — otherwise
	// quitting right after /new resumes the *old* session on next launch.
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to persist the new (empty) session immediately")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("cmd() = %v, want nil (no save error)", msg)
	}
	_, _, resumedID, ok, corrupt := session.LoadLatest(workDir)
	if !ok || corrupt {
		t.Fatalf("LoadLatest after /new: ok=%v corrupt=%v, want ok=true corrupt=false", ok, corrupt)
	}
	if resumedID != got.sessionID {
		t.Errorf("LoadLatest resumed id = %q, want the new session's id %q", resumedID, got.sessionID)
	}
}

func TestHandleGitCommand(t *testing.T) {
	t.Run("status with no args", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand(nil)
		lines := got.renderedLines()
		if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
			t.Errorf("lines = %+v, want a usage line for a bare /git", lines)
		}
	})

	t.Run("auto with no on/off shows current state", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand([]string{"auto"})
		lines := got.renderedLines()
		if len(lines) != 1 || !strings.Contains(lines[0], "off") {
			t.Errorf("lines = %+v, want it to report auto-commit is off", lines)
		}
	})

	t.Run("refuses to turn on outside a git repo", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand([]string{"auto", "on"})
		if got.autoCommit {
			t.Error("autoCommit = true outside a git repo, want it to refuse")
		}
		lines := got.renderedLines()
		if len(lines) != 1 || !strings.Contains(lines[0], "git repository") {
			t.Errorf("lines = %+v, want an error mentioning it's not a git repo", lines)
		}
	})

	t.Run("turns on inside a real git repo", func(t *testing.T) {
		m := Model{workDir: initTestRepo(t)}
		got := m.handleGitCommand([]string{"auto", "on"})
		if !got.autoCommit {
			t.Error("autoCommit = false, want true after /git auto on in a real repo")
		}

		got = got.handleGitCommand([]string{"auto", "off"})
		if got.autoCommit {
			t.Error("autoCommit = true, want false after /git auto off")
		}
	})
}

func TestCommitMessage(t *testing.T) {
	short := commitMessage("fix the bug")
	if short != "chisel: fix the bug" {
		t.Errorf("commitMessage = %q", short)
	}

	long := commitMessage(strings.Repeat("a", 100))
	// 72-char subject + "…" (3 bytes in UTF-8), prefixed with "chisel: ".
	if want := len("chisel: ") + 72 + len("…"); len(long) != want {
		t.Errorf("commitMessage len = %d, want %d (got %q)", len(long), want, long)
	}
}

func TestLastUserText(t *testing.T) {
	messages := []agent.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "reply2"},
	}
	if got := lastUserText(messages); got != "second" {
		t.Errorf("lastUserText = %q, want %q", got, "second")
	}
	if got := lastUserText(nil); got != "changes" {
		t.Errorf("lastUserText(nil) = %q, want the fallback %q", got, "changes")
	}
}
