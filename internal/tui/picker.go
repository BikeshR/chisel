package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// refreshCommandPalette recomputes the live "/"-command dropdown from
// the textarea's current content — the same prefix/no-space condition
// tab-completion used to check (a slash command is only ever the
// first, and while still being typed only, token). Called after every
// keystroke that could have changed the textarea while composing one,
// so the dropdown always reflects exactly what's typed rather than
// only updating on tab.
//
// Only calls recomputeViewportHeight when the candidate count actually
// changed — that's the only thing about this that affects layout, and
// recomputeViewportHeight derives the viewport's height from m.height
// (the real terminal size), so calling it unconditionally on every
// single keystroke recomputed (and could disturb) the viewport's height
// even while just typing a plain, non-"/" message with nothing to
// recompute at all.
func (m *Model) refreshCommandPalette() {
	before := len(m.commandPaletteCandidates)
	value := strings.TrimRight(m.textArea.Value(), "\n")
	if strings.HasPrefix(value, "/") && !strings.ContainsAny(value, " \t\n") {
		var matches []string
		for _, name := range m.commandNames() {
			if strings.HasPrefix(name, value) {
				matches = append(matches, name)
			}
		}
		m.commandPaletteCandidates = matches
		if m.commandPaletteSelected >= len(matches) {
			m.commandPaletteSelected = 0
		}
	} else {
		m.commandPaletteCandidates = nil
		m.commandPaletteSelected = 0
	}
	if len(m.commandPaletteCandidates) != before {
		m.recomputeViewportHeight()
	}
}

// movePaletteSelection moves the command palette's highlighted row by
// delta, wrapping around at either end.
func (m *Model) movePaletteSelection(delta int) {
	m.commandPaletteSelected = wrapIndex(m.commandPaletteSelected, delta, len(m.commandPaletteCandidates))
}

// acceptPaletteCandidate fills the textarea with the currently
// highlighted command-palette candidate (plus a trailing space, ready
// for arguments) and closes the palette — shared by tab (fill and keep
// composing) and enter (fill, then fall through to submit immediately).
func (m *Model) acceptPaletteCandidate() {
	name := m.commandPaletteCandidates[m.commandPaletteSelected]
	m.textArea.SetValue(name + " ")
	m.textArea.CursorEnd()
	m.commandPaletteCandidates = nil
	m.commandPaletteSelected = 0
	m.recomputeViewportHeight()
}

// renderCommandPalette renders the live "/"-command dropdown as a
// block just above the input line — a "dropup," since the input sits
// at the bottom of the screen — highlighting the currently selected
// candidate. Empty if the palette isn't showing (see
// refreshCommandPalette).
func (m Model) renderCommandPalette() string {
	if len(m.commandPaletteCandidates) == 0 {
		return ""
	}
	lines := make([]string, len(m.commandPaletteCandidates))
	for i, name := range m.commandPaletteCandidates {
		if i == m.commandPaletteSelected {
			lines[i] = pickerSelectedStyle.Render("› " + name)
		} else {
			lines[i] = dimStyle.Render("  " + name)
		}
	}
	return strings.Join(lines, "\n")
}

// handleModelPickerKey drives /model's interactive picker (see
// modelPickerActive): up/down moves the highlighted model (wrapping
// around), enter switches to it and closes the picker, esc cancels
// without changing anything. A sub-mode of stateInput, checked before
// any of its other key handling, the same precedence
// reverseSearchActive already has.
func (m Model) handleModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	models := agent.KnownModels()
	switch {
	case msg.Type == tea.KeyUp:
		m.modelPickerSelected = wrapIndex(m.modelPickerSelected, -1, len(models))
		return m, nil
	case msg.Type == tea.KeyDown:
		m.modelPickerSelected = wrapIndex(m.modelPickerSelected, 1, len(models))
		return m, nil
	case msg.Type == tea.KeyEsc:
		m.modelPickerActive = false
		m.recomputeViewportHeight()
		return m, nil
	case msg.Type == tea.KeyEnter && !msg.Alt:
		m.modelPickerActive = false
		m.recomputeViewportHeight()
		if m.modelPickerSelected >= 0 && m.modelPickerSelected < len(models) {
			name := models[m.modelPickerSelected]
			m.client.SetModel(name)
			m.appendLine(dimStyle.Render("switched to " + name))
		}
		return m, nil
	}
	return m, nil
}

// renderModelPicker renders /model's interactive picker in place of the
// textarea (see modelPickerActive) — a header line plus one row per
// known model, the current one marked and the highlighted row styled
// the same way the command palette's own selection is.
func (m Model) renderModelPicker() string {
	models := agent.KnownModels()
	current := m.client.ModelName()
	lines := make([]string, 0, len(models)+1)
	lines = append(lines, dimStyle.Render("select a model — ↑/↓ to move, enter to switch, esc to cancel:"))
	for i, name := range models {
		marker := "  "
		if name == current {
			marker = "› "
		}
		line := marker + name
		if i == m.modelPickerSelected {
			lines = append(lines, pickerSelectedStyle.Render(line))
		} else {
			lines = append(lines, dimStyle.Render(line))
		}
	}
	return strings.Join(lines, "\n")
}

// wrapIndex moves idx by delta within [0, n), wrapping around at
// either end — shared by the command palette and the model picker, the
// two "arrow through a list of strings" pickers.
func wrapIndex(idx, delta, n int) int {
	if n == 0 {
		return 0
	}
	idx = (idx + delta) % n
	if idx < 0 {
		idx += n
	}
	return idx
}
