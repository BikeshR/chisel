package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/history"
)

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestCtrlRStartsReverseSearch(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build ./...", "git status"}

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	gotModel := got.(Model)
	if !gotModel.reverseSearchActive {
		t.Fatal("expected ctrl+r to activate reverse search")
	}
}

func TestCtrlRNoOpWithEmptyHistory(t *testing.T) {
	m := newInputModel()
	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	gotModel := got.(Model)
	if gotModel.reverseSearchActive {
		t.Error("expected ctrl+r to be a no-op with no history to search")
	}
}

func TestReverseSearchFindsMostRecentMatch(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build ./...", "go test ./...", "git status"}
	m.reverseSearchActive = true

	got, _ := m.handleKey(runeKey('g'))
	got, _ = got.(Model).handleKey(runeKey('o'))
	gotModel := got.(Model)

	if gotModel.reverseSearchMatchIdx != 1 {
		t.Errorf("matchIdx = %d, want 1 (\"go test ./...\", the most recent match for \"go\")", gotModel.reverseSearchMatchIdx)
	}
}

func TestReverseSearchCtrlRStepsToOlderMatch(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build ./...", "go test ./...", "git status"}
	m.reverseSearchActive = true
	m.reverseSearchQuery = "go"
	m.reverseSearchMatchIdx = 1

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	gotModel := got.(Model)
	if gotModel.reverseSearchMatchIdx != 0 {
		t.Errorf("matchIdx = %d, want 0 (the next older match)", gotModel.reverseSearchMatchIdx)
	}
}

func TestReverseSearchNoMatchShowsFailed(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build ./..."}
	m.reverseSearchActive = true

	got, _ := m.handleKey(runeKey('z'))
	gotModel := got.(Model)
	if gotModel.reverseSearchMatchIdx != -1 {
		t.Errorf("matchIdx = %d, want -1 for no match", gotModel.reverseSearchMatchIdx)
	}
	if !strings.Contains(gotModel.reverseSearchLine(), "failed") {
		t.Errorf("reverseSearchLine() = %q, want it to indicate a failed search", gotModel.reverseSearchLine())
	}
}

func TestReverseSearchEscCancelsWithoutChangingTextarea(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build ./..."}
	m.textArea.SetValue("original draft")
	m.reverseSearchActive = true
	m.reverseSearchQuery = "go"
	m.reverseSearchMatchIdx = 0

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel := got.(Model)
	if gotModel.reverseSearchActive {
		t.Error("expected esc to deactivate reverse search")
	}
	if gotModel.textArea.Value() != "original draft" {
		t.Errorf("textArea.Value() = %q, want the original draft left untouched", gotModel.textArea.Value())
	}
}

func TestReverseSearchEnterAcceptsAndSubmits(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"echo hello"}
	m.reverseSearchActive = true
	m.reverseSearchQuery = "echo"
	m.reverseSearchMatchIdx = 0

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter to submit the matched history entry")
	}
	gotModel := got.(Model)
	if gotModel.reverseSearchActive {
		t.Error("expected enter to deactivate reverse search")
	}
	if len(gotModel.messages) != 1 || gotModel.messages[0].Content != "echo hello" {
		t.Errorf("messages = %+v, want the matched entry submitted", gotModel.messages)
	}
}

func TestReverseSearchEnterWithNoMatchDoesNothing(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"echo hello"}
	m.reverseSearchActive = true
	m.reverseSearchQuery = "zzz"
	m.reverseSearchMatchIdx = -1

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no Cmd when accepting an empty/failed search")
	}
	gotModel := got.(Model)
	if gotModel.reverseSearchActive {
		t.Error("expected enter to deactivate reverse search even with no match")
	}
	if len(gotModel.messages) != 0 {
		t.Errorf("messages = %+v, want none sent", gotModel.messages)
	}
}

func TestRecordHistoryPersistsNewEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newInputModel()

	cmd := m.recordHistory("go test ./...")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to persist a new history entry")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("cmd() = %v, want nil (no error persisting)", msg)
	}

	if got := history.Load(); len(got) != 1 || got[0] != "go test ./..." {
		t.Errorf("history.Load() = %+v, want [\"go test ./...\"]", got)
	}
}

func TestRecordHistorySkipsDuplicateOfLastEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newInputModel()
	m.inputHistory = []string{"go test ./..."}

	cmd := m.recordHistory("go test ./...")
	if cmd != nil {
		t.Error("expected a nil Cmd for a duplicate of the last entry")
	}
	if len(m.inputHistory) != 1 {
		t.Errorf("inputHistory = %+v, want no duplicate appended", m.inputHistory)
	}
}

func TestReverseSearchBackspaceNarrowsQuery(t *testing.T) {
	m := newInputModel()
	m.inputHistory = []string{"go build"}
	m.reverseSearchActive = true
	m.reverseSearchQuery = "gox"
	m.reverseSearchMatchIdx = -1

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	gotModel := got.(Model)
	if gotModel.reverseSearchQuery != "go" {
		t.Errorf("query = %q, want %q", gotModel.reverseSearchQuery, "go")
	}
	if gotModel.reverseSearchMatchIdx != 0 {
		t.Errorf("matchIdx = %d, want 0 after backspace re-matches \"go\"", gotModel.reverseSearchMatchIdx)
	}
}
