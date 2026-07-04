package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

func newInputModel() Model {
	m := Model{state: stateInput, client: agent.New("minimax-m3")}
	m.textArea = textarea.New()
	m.textArea.Focus()
	m.textArea.KeyMap.InsertNewline.SetKeys("alt+enter")
	return m
}

func TestPlainEnterSubmits(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("hello chisel")

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — plain enter should submit")
	}
	gotModel := got.(Model)
	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel after submitting", gotModel.state)
	}
	if len(gotModel.messages) != 1 || gotModel.messages[0].Content != "hello chisel" {
		t.Errorf("messages = %+v, want a single user message with the typed text", gotModel.messages)
	}
}

func TestAltEnterInsertsNewlineInsteadOfSubmitting(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("first line")

	// textarea.Update may itself return a non-nil Cmd (e.g. cursor
	// blink) — that's expected and not what this test is checking; what
	// matters is that state didn't advance to stateWaitingModel.
	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	gotModel := got.(Model)
	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput — alt+enter must not submit", gotModel.state)
	}
	if len(gotModel.messages) != 0 {
		t.Errorf("messages = %+v, want none — alt+enter shouldn't send anything", gotModel.messages)
	}
	if !strings.Contains(gotModel.textArea.Value(), "\n") {
		t.Errorf("textArea value = %q, want alt+enter to have inserted a newline", gotModel.textArea.Value())
	}
}

func TestMultiLineMessageSubmitsAsOneUserMessage(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("line one\nline two\nline three")

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	gotModel := got.(Model)
	if len(gotModel.messages) != 1 {
		t.Fatalf("messages = %+v, want exactly one user message", gotModel.messages)
	}
	want := "line one\nline two\nline three"
	if gotModel.messages[0].Content != want {
		t.Errorf("message content = %q, want %q", gotModel.messages[0].Content, want)
	}
}

func TestSubmitTrimsTrailingNewlineLeftByAltEnter(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("done typing\n") // as if the user hit alt+enter then enter with nothing after

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)
	if len(gotModel.messages) != 1 || gotModel.messages[0].Content != "done typing" {
		t.Errorf("messages = %+v, want the trailing blank line trimmed", gotModel.messages)
	}
}

func TestSubmitClearsTextArea(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("some text")

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)
	if gotModel.textArea.Value() != "" {
		t.Errorf("textArea value after submit = %q, want empty", gotModel.textArea.Value())
	}
}
