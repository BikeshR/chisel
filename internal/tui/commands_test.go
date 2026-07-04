package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/session"
)

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
