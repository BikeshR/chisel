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
		if len(gotModel.lines) != 1 || !strings.Contains(gotModel.lines[0], "minimax-m3") || !strings.Contains(gotModel.lines[0], "ok") {
			t.Errorf("lines = %+v, want a line mentioning the model and its reply", gotModel.lines)
		}
	})

	t.Run("failure", func(t *testing.T) {
		m := Model{state: stateWaitingModel}
		got, _ := m.handleModelCheckResult(modelCheckResultMsg{model: "kimi-k2.6", err: errors.New("upstream request failed")})
		gotModel := got.(Model)

		if gotModel.state != stateInput {
			t.Errorf("state = %v, want stateInput", gotModel.state)
		}
		if len(gotModel.lines) != 1 || !strings.Contains(gotModel.lines[0], "kimi-k2.6") || !strings.Contains(gotModel.lines[0], "upstream request failed") {
			t.Errorf("lines = %+v, want a line mentioning the model and the error", gotModel.lines)
		}
	})
}

func TestHandleNewCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := "/home/brana/code/testproj"

	if err := session.Save(workDir, []agent.Message{{Role: "user", Content: "old conversation"}}); err != nil {
		t.Fatal(err)
	}

	m := Model{
		workDir:  workDir,
		messages: []agent.Message{{Role: "user", Content: "old conversation"}},
		lines:    []string{"you  old conversation"},
	}
	got := m.handleNewCommand()

	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want empty", got.messages)
	}
	if len(got.lines) != 1 || !strings.Contains(got.lines[0], "new session") {
		t.Errorf("lines = %+v, want a single line announcing a new session", got.lines)
	}
	if _, _, ok := session.Load(workDir); ok {
		t.Error("session.Load after /new: ok = true, want the saved session cleared")
	}
}

func TestHandleGitCommand(t *testing.T) {
	t.Run("status with no args", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand(nil)
		if len(got.lines) != 1 || !strings.Contains(got.lines[0], "usage") {
			t.Errorf("lines = %+v, want a usage line for a bare /git", got.lines)
		}
	})

	t.Run("auto with no on/off shows current state", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand([]string{"auto"})
		if len(got.lines) != 1 || !strings.Contains(got.lines[0], "off") {
			t.Errorf("lines = %+v, want it to report auto-commit is off", got.lines)
		}
	})

	t.Run("refuses to turn on outside a git repo", func(t *testing.T) {
		m := Model{workDir: t.TempDir()}
		got := m.handleGitCommand([]string{"auto", "on"})
		if got.autoCommit {
			t.Error("autoCommit = true outside a git repo, want it to refuse")
		}
		if len(got.lines) != 1 || !strings.Contains(got.lines[0], "git repository") {
			t.Errorf("lines = %+v, want an error mentioning it's not a git repo", got.lines)
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
