package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// bigTranscript builds a Model with enough entries to scroll — one line
// per string in lines, a small viewport, and enough terminal width set
// for wrapping to behave (width 0 would pass content through unwrapped).
func bigTranscript(t *testing.T, n int) Model {
	t.Helper()
	m := Model{width: 80}
	m.viewport = viewport.New(80, 5)
	m.textArea = textarea.New()
	for i := 0; i < n; i++ {
		m.appendLine(strings.Repeat("x", 10))
	}
	return m
}

func TestPageUpScrollsAwayFromBottom(t *testing.T) {
	m := bigTranscript(t, 50)
	if !m.viewport.AtBottom() {
		t.Fatal("expected a freshly built transcript to start at the bottom")
	}

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	gotModel := got.(Model)
	if gotModel.viewport.AtBottom() {
		t.Error("expected PgUp to scroll away from the bottom")
	}
}

func TestPageDownScrollsBackToBottom(t *testing.T) {
	m := bigTranscript(t, 50)
	m.viewport.GotoTop()

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	gotModel := got.(Model)
	if gotModel.viewport.YOffset <= 0 {
		t.Error("expected PgDown to move forward from the top")
	}
}

func TestCtrlUScrollsOutsideStateInput(t *testing.T) {
	m := bigTranscript(t, 50)
	m.state = stateWaitingModel

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	gotModel := got.(Model)
	if gotModel.viewport.AtBottom() {
		t.Error("expected ctrl+u to scroll up while busy (not stateInput)")
	}
}

func TestCtrlUEditsTextInsteadOfScrollingInStateInput(t *testing.T) {
	m := bigTranscript(t, 50)
	m.state = stateInput
	m.textArea.SetValue("hello")

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	gotModel := got.(Model)
	// ctrl+u is textinput's "delete before cursor" binding — it must
	// reach the text input, not be swallowed by transcript scrolling.
	if !gotModel.viewport.AtBottom() {
		t.Error("ctrl+u in stateInput scrolled the transcript, want it left at the bottom untouched")
	}
}

func TestStickyBottomStaysAtBottomWhileAppending(t *testing.T) {
	m := Model{width: 80}
	m.viewport = viewport.New(80, 5)
	for i := 0; i < 20; i++ {
		m.appendLine(strings.Repeat("line ", 3))
	}
	if !m.viewport.AtBottom() {
		t.Fatal("expected to still be at the bottom after appending while already stuck there")
	}
}

func TestScrolledUpIsNotYankedBackByNewContent(t *testing.T) {
	m := bigTranscript(t, 50)
	m.viewport.GotoTop()
	if m.viewport.AtBottom() {
		t.Fatal("GotoTop should have left the viewport away from the bottom")
	}

	m.appendLine("one more line arrives while scrolled up")
	if m.viewport.AtBottom() {
		t.Error("appending content while scrolled up should not force the view back to the bottom")
	}
}

func TestMouseWheelScrolls(t *testing.T) {
	m := bigTranscript(t, 50)
	if !m.viewport.AtBottom() {
		t.Fatal("expected a freshly built transcript to start at the bottom")
	}

	got, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	gotModel := got.(Model)
	if gotModel.viewport.AtBottom() {
		t.Error("expected a mouse wheel-up event to scroll away from the bottom")
	}
}
