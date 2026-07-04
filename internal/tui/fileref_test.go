package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestExpandFileReferencesInjectsContent(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("the secret is banana"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _ := expandFileReferences(workDir, "check @notes.txt please")
	if !strings.Contains(got, "the secret is banana") {
		t.Errorf("got %q, want the file's content injected", got)
	}
	if !strings.Contains(got, "notes.txt") {
		t.Errorf("got %q, want the path mentioned in the injected block", got)
	}
	if !strings.Contains(got, "please") {
		t.Errorf("got %q, want the surrounding text preserved", got)
	}
}

func TestExpandFileReferencesLeavesUnresolvableTokenAsLiteralText(t *testing.T) {
	workDir := t.TempDir()
	got, _ := expandFileReferences(workDir, "ask @someone about this")
	if got != "ask @someone about this" {
		t.Errorf("got %q, want the unresolvable @token left unchanged", got)
	}
}

func TestExpandFileReferencesRejectsEscapingPaths(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _ := expandFileReferences(workDir, "look at @../../../etc/passwd")
	if strings.Contains(got, "root:") {
		t.Error("expandFileReferences read a file outside workDir")
	}
}

func TestExpandFileReferencesHandlesMultipleReferences(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("AAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "b.txt"), []byte("BBB"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _ := expandFileReferences(workDir, "compare @a.txt and @b.txt")
	if !strings.Contains(got, "AAA") || !strings.Contains(got, "BBB") {
		t.Errorf("got %q, want both files' content injected", got)
	}
}

// TestExpandFileReferencesCapsInjectedContent is the regression test for
// a real gap: a tool result is capped at agent.maxToolOutputChars
// precisely because oversized content gets resent on every subsequent
// request, but an @-referenced file bypassed that entirely and
// invisibly (the transcript only ever shows what was typed, never the
// expansion) — the only unbounded path into the context window.
func TestExpandFileReferencesCapsInjectedContent(t *testing.T) {
	workDir := t.TempDir()
	huge := strings.Repeat("x", 50_000)
	if err := os.WriteFile(filepath.Join(workDir, "huge.txt"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}

	got, truncated := expandFileReferences(workDir, "look at @huge.txt")
	if len(truncated) != 1 || truncated[0] != "huge.txt" {
		t.Errorf("truncated = %+v, want [\"huge.txt\"]", truncated)
	}
	if strings.Count(got, "x") >= 50_000 {
		t.Errorf("expanded content has %d 'x' characters, want it capped well below the full 50000", strings.Count(got, "x"))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("want a truncation marker in the injected content")
	}
}

func TestExpandFileReferencesSmallFileNotFlaggedTruncated(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "small.txt"), []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, truncated := expandFileReferences(workDir, "look at @small.txt")
	if len(truncated) != 0 {
		t.Errorf("truncated = %+v, want none for a small file", truncated)
	}
}

func TestSubmitTextShowsOriginalButSendsExpanded(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("file content here"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newInputModel()
	m.workDir = workDir

	gotModel, _ := m.submitText("look at @notes.txt")

	lines := gotModel.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "@notes.txt") || strings.Contains(lines[0], "file content here") {
		t.Errorf("transcript lines = %+v, want the original @notes.txt text shown, not the expanded content", lines)
	}

	if len(gotModel.messages) != 1 || !strings.Contains(gotModel.messages[0].Content, "file content here") {
		t.Errorf("messages = %+v, want the expanded content sent to the model", gotModel.messages)
	}
}

// TestSubmitTextNotesTruncatedFileReference confirms the user, not just
// the model, learns when an @-referenced file got capped — the
// transcript otherwise never shows the expansion at all.
func TestSubmitTextNotesTruncatedFileReference(t *testing.T) {
	workDir := t.TempDir()
	huge := strings.Repeat("x", 50_000)
	if err := os.WriteFile(filepath.Join(workDir, "huge.txt"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newInputModel()
	m.workDir = workDir

	gotModel, _ := m.submitText("look at @huge.txt")

	found := false
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "huge.txt") && strings.Contains(l, "truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a note that huge.txt was truncated", gotModel.renderedLines())
	}
}

func TestListFilesForCompletionFiltersByPrefixAndSkipsDirs(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "manager.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "other.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "node_modules", "manifest.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listFilesForCompletion(workDir, "ma")
	if len(got) != 2 || got[0] != "main.go" || got[1] != "manager.go" {
		t.Errorf("got %+v, want [main.go manager.go], sorted, node_modules excluded", got)
	}
}

func TestCompleteFileReferenceSingleMatch(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := completeFileReference(workDir, "read")
	if !ok || got != "readme.md" {
		t.Errorf("completeFileReference = (%q, %v), want (readme.md, true)", got, ok)
	}
}

func TestCompleteFileReferenceMultipleMatchesCompletesToCommonPrefix(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main_test.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := completeFileReference(workDir, "ma")
	if !ok || got != "main" {
		t.Errorf("completeFileReference = (%q, %v), want (main, true) — the longest common prefix of main.go/main_test.go", got, ok)
	}
}

func TestCompleteFileReferenceNoMatches(t *testing.T) {
	workDir := t.TempDir()
	if _, ok := completeFileReference(workDir, "nonexistent"); ok {
		t.Error("expected ok = false when nothing matches")
	}
}

func TestTabCompletesFileReferenceInInput(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newInputModel()
	m.workDir = workDir
	m.textArea.SetValue("check @read")

	gotTeaModel, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	got := gotTeaModel.(Model)

	if got.textArea.Value() != "check @readme.md" {
		t.Errorf("textArea value = %q, want %q", got.textArea.Value(), "check @readme.md")
	}
}

func TestTabWithNoFileReferenceDoesNothingSpecial(t *testing.T) {
	m := newInputModel()
	m.textArea.SetValue("just some text")

	gotTeaModel, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	got := gotTeaModel.(Model)

	if got.textArea.Value() != "just some text" {
		t.Errorf("textArea value = %q, want unchanged", got.textArea.Value())
	}
}
