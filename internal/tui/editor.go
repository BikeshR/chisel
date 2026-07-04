package tui

import (
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// editorDoneMsg carries the outcome of composing input in an external
// $EDITOR (see startExternalEditor) — the temp file's path, so Update can
// read back whatever was saved there and clean it up.
type editorDoneMsg struct {
	path string
	err  error
}

// startExternalEditor suspends the TUI (via tea.ExecProcess, which pauses
// the whole Program until the child process exits) to open $EDITOR —
// falling back to vi, the same default `git commit`, `crontab -e`, and
// most other command-line tools use when the variable isn't set — on a
// temp file seeded with the textarea's current content. Useful for
// composing something long or carefully formatted with a real editor's
// own line editing rather than bubbles' minimal textarea.
func startExternalEditor(initial string) tea.Cmd {
	f, err := os.CreateTemp("", "chisel-input-*.md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{err: err} }
	}
	path := f.Name()

	_, writeErr := f.WriteString(initial)
	closeErr := f.Close()
	if err := writeErr; err != nil {
		return func() tea.Msg { return editorDoneMsg{path: path, err: err} }
	}
	if closeErr != nil {
		return func() tea.Msg { return editorDoneMsg{path: path, err: closeErr} }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

// handleEditorDone loads the external editor's saved content back into
// the textarea and removes the temp file — best-effort: a failure here
// (the editor exiting non-zero, the temp file having vanished) leaves
// whatever was already in the textarea untouched rather than losing it.
func (m Model) handleEditorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if msg.path != "" {
		defer func() { _ = os.Remove(msg.path) }()
	}
	if msg.err != nil {
		m.appendLine(errorStyle.Render("editor: " + msg.err.Error()))
		return m, textarea.Blink
	}
	content, err := os.ReadFile(msg.path)
	if err != nil {
		m.appendLine(errorStyle.Render("editor: " + err.Error()))
		return m, textarea.Blink
	}
	m.textArea.SetValue(strings.TrimRight(string(content), "\n"))
	m.textArea.CursorEnd()
	return m, textarea.Blink
}
