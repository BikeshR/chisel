package tui

import (
	"errors"
	"strings"
	"testing"
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
