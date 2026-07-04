package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// TestTypingSlashShowsAllCommands is the direct test of the feature as
// requested: typing "/" alone (every command name starts with it) opens
// the live dropdown listing every known "/"-command.
func TestTypingSlashShowsAllCommands(t *testing.T) {
	m := newInputModel()

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	gotModel := got.(Model)

	if len(gotModel.commandPaletteCandidates) != len(gotModel.commandNames()) {
		t.Errorf("commandPaletteCandidates = %+v, want every command name shown", gotModel.commandPaletteCandidates)
	}
}

// TestTypingSlashCommandPrefixNarrowsCandidates confirms the dropdown
// live-filters as more is typed, not just on tab.
func TestTypingSlashCommandPrefixNarrowsCandidates(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/mo")
	m.refreshCommandPalette()

	if len(m.commandPaletteCandidates) != 1 || m.commandPaletteCandidates[0] != "/model" {
		t.Errorf("commandPaletteCandidates = %+v, want just [\"/model\"]", m.commandPaletteCandidates)
	}
}

// TestCommandPaletteClosesOnceASpaceFollows mirrors the old
// tab-completion's own trigger condition: a slash command is only ever
// the first, and while still being typed only, token.
func TestCommandPaletteClosesOnceASpaceFollows(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/resume 1")
	m.refreshCommandPalette()

	if len(m.commandPaletteCandidates) != 0 {
		t.Errorf("commandPaletteCandidates = %+v, want none once a space follows the command", m.commandPaletteCandidates)
	}
}

// TestCommandPaletteEmptyForUnknownPrefix confirms typing something
// that matches no command at all just leaves the dropdown empty rather
// than erroring or showing something misleading.
func TestCommandPaletteEmptyForUnknownPrefix(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/xyz")
	m.refreshCommandPalette()

	if len(m.commandPaletteCandidates) != 0 {
		t.Errorf("commandPaletteCandidates = %+v, want none for an unrecognized prefix", m.commandPaletteCandidates)
	}
}

// TestCommandPaletteUpDownNavigatesInsteadOfHistory is the regression
// test for the key precedence: up/down must browse the dropdown while
// it's showing, not recall input history the way they normally do in
// stateInput.
func TestCommandPaletteUpDownNavigatesInsteadOfHistory(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"some old message"}
	m.textArea.SetValue("/")
	m.refreshCommandPalette()
	start := m.commandPaletteSelected

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	gotModel := got.(Model)

	if gotModel.commandPaletteSelected == start {
		t.Error("expected down to move the palette selection")
	}
	if gotModel.textArea.Value() != "/" {
		t.Errorf("textArea.Value() = %q, want unchanged — down should navigate the palette, not recall history", gotModel.textArea.Value())
	}
}

// TestCommandPaletteSelectionWrapsAround confirms moving past either
// end wraps rather than stopping dead.
func TestCommandPaletteSelectionWrapsAround(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/")
	m.refreshCommandPalette()
	n := len(m.commandPaletteCandidates)

	m.movePaletteSelection(-1)
	if m.commandPaletteSelected != n-1 {
		t.Errorf("selected = %d, want %d (wrapped to the last candidate)", m.commandPaletteSelected, n-1)
	}
	m.movePaletteSelection(1)
	if m.commandPaletteSelected != 0 {
		t.Errorf("selected = %d, want 0 (wrapped back to the first)", m.commandPaletteSelected)
	}
}

// TestCommandPaletteEscDismissesWithoutClearingText confirms esc closes
// the dropdown but leaves whatever was typed untouched.
func TestCommandPaletteEscDismissesWithoutClearingText(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/mo")
	m.refreshCommandPalette()

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel := got.(Model)

	if len(gotModel.commandPaletteCandidates) != 0 {
		t.Error("expected esc to dismiss the palette")
	}
	if gotModel.textArea.Value() != "/mo" {
		t.Errorf("textArea.Value() = %q, want unchanged by esc", gotModel.textArea.Value())
	}
}

// TestCommandPaletteTabAcceptsHighlightedCandidateAndKeepsComposing is
// the regression test for tab's new behavior: it must fill in the
// highlighted candidate (not just the longest common prefix of
// everything matching) and leave the palette closed so args can follow,
// without submitting.
func TestCommandPaletteTabAcceptsHighlightedCandidateAndKeepsComposing(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/re")
	m.refreshCommandPalette()
	// "/resume" and "/retry" and "/rewind" all match "/re" — move to a
	// specific one deterministically rather than relying on whichever
	// sorts first.
	for i, name := range m.commandPaletteCandidates {
		if name == "/rewind" {
			m.commandPaletteSelected = i
		}
	}

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	gotModel := got.(Model)

	if cmd != nil {
		t.Error("expected tab to just fill the textarea, not submit")
	}
	if gotModel.textArea.Value() != "/rewind " {
		t.Errorf("textArea.Value() = %q, want %q", gotModel.textArea.Value(), "/rewind ")
	}
	if len(gotModel.commandPaletteCandidates) != 0 {
		t.Error("expected the palette to close after accepting a candidate")
	}
}

// TestCommandPaletteEnterAcceptsAndSubmitsImmediately is the direct
// test of the requested "/model + enter"-style flow generalized to
// every command: enter while the dropdown is showing runs the
// highlighted candidate right away, not just the literal (possibly
// still-ambiguous) text that was typed.
func TestCommandPaletteEnterAcceptsAndSubmitsImmediately(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("/stat") // unambiguously "/status"
	m.refreshCommandPalette()

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)

	if len(gotModel.commandPaletteCandidates) != 0 {
		t.Error("expected the palette to close once enter submits")
	}
	found := false
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "workdir") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want /status to have actually run", gotModel.renderedLines())
	}
}

// TestModelCommandOpensInteractivePicker is the direct test of the
// requested "/model + enter shows a list to choose from, not static
// text" behavior.
func TestModelCommandOpensInteractivePicker(t *testing.T) {
	m := newInputModel()
	m.client.SetModel("glm-5.2")

	got, cmd := m.handleModelCommand(nil)
	if cmd != nil {
		t.Error("expected a nil Cmd — opening the picker is synchronous")
	}
	if !got.modelPickerActive {
		t.Fatal("expected /model with no args to open the interactive picker")
	}
	if len(got.renderedLines()) != 0 {
		t.Errorf("lines = %+v, want no static list printed to the transcript anymore", got.renderedLines())
	}

	models := agent.KnownModels()
	if got.modelPickerSelected < 0 || got.modelPickerSelected >= len(models) || models[got.modelPickerSelected] != "glm-5.2" {
		t.Errorf("modelPickerSelected = %d, want it pre-selecting the current model (glm-5.2)", got.modelPickerSelected)
	}
}

// TestModelPickerUpDownWrapsAndEnterSwitches drives the picker's full
// key handling: navigate, then confirm.
func TestModelPickerUpDownWrapsAndEnterSwitches(t *testing.T) {
	m := newInputModel()
	m.modelPickerActive = true
	m.modelPickerSelected = 0

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	gotModel := got.(Model)
	if gotModel.modelPickerSelected != len(agent.KnownModels())-1 {
		t.Errorf("modelPickerSelected = %d, want wrapped to the last model after up from 0", gotModel.modelPickerSelected)
	}

	got, _ = gotModel.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel = got.(Model)
	if gotModel.modelPickerActive {
		t.Error("expected enter to close the picker")
	}
	want := agent.KnownModels()[len(agent.KnownModels())-1]
	if gotModel.client.ModelName() != want {
		t.Errorf("ModelName() = %q, want %q", gotModel.client.ModelName(), want)
	}
	found := false
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "switched to "+want) {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line confirming the switch", gotModel.renderedLines())
	}
}

// TestModelPickerEscCancelsWithoutSwitching confirms esc leaves the
// current model untouched.
func TestModelPickerEscCancelsWithoutSwitching(t *testing.T) {
	m := newInputModel()
	m.client.SetModel("minimax-m3")
	m.modelPickerActive = true
	m.modelPickerSelected = 3 // some other model entirely

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel := got.(Model)

	if gotModel.modelPickerActive {
		t.Error("expected esc to close the picker")
	}
	if gotModel.client.ModelName() != "minimax-m3" {
		t.Errorf("ModelName() = %q, want unchanged by esc", gotModel.client.ModelName())
	}
}

// TestViewRendersCommandPaletteAndModelPickerWithoutPanicking is a
// smoke test that both new UI states render through the real View()
// without panicking or leaving the layout in a broken state — the
// closest thing to an interactive check available without a real
// terminal.
func TestViewRendersCommandPaletteAndModelPickerWithoutPanicking(t *testing.T) {
	m := newInputModel()
	m.width, m.height = 80, 24
	m.recomputeViewportHeight()

	m.textArea.SetValue("/")
	m.refreshCommandPalette()
	if out := m.View(); !strings.Contains(out, "/help") {
		t.Errorf("View() with the command palette open = %q, want it to list command names", out)
	}

	m.commandPaletteCandidates = nil
	m.modelPickerActive = true
	m.recomputeViewportHeight()
	if out := m.View(); !strings.Contains(out, m.client.ModelName()) {
		t.Errorf("View() with the model picker open = %q, want it to list the current model", out)
	}
}
