package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleEditorDoneLoadsContentAndCleansUpTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chisel-input-test.md")
	if err := os.WriteFile(path, []byte("edited message\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newInputModel()
	updated, _ := m.handleEditorDone(editorDoneMsg{path: path})
	got := updated.(Model)

	if got.textArea.Value() != "edited message" {
		t.Errorf("textArea.Value() = %q, want %q", got.textArea.Value(), "edited message")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected temp file %q to be removed, stat err = %v", path, err)
	}
}

func TestHandleEditorDoneReportsError(t *testing.T) {
	m := newInputModel()
	updated, _ := m.handleEditorDone(editorDoneMsg{err: os.ErrPermission})
	got := updated.(Model)

	if len(got.entries) != 1 || !strings.Contains(got.renderedLines()[0], "editor:") {
		t.Errorf("expected an editor error line in the transcript, got entries: %+v", got.entries)
	}
}

// TestEditorCommandSupportsEditorWithArguments is the regression test
// for a real bug: exec.Command(editor, path) treated a multi-word
// $EDITOR (EDITOR="code -w" and EDITOR="emacsclient -t" are both common)
// as a single literal, nonexistent binary name including the space,
// failing to exec at all. editorCommand must run through a shell so
// $EDITOR's own arguments are parsed the way a real shell invocation
// would. "wc -c" is a genuine two-word command (real binary + a real
// flag) — if the fix isn't in place, this fails outright rather than
// counting the file's bytes.
func TestEditorCommandSupportsEditorWithArguments(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := editorCommand("wc -c", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd.CombinedOutput: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "5") {
		t.Errorf("output = %q, want it to report the file's byte count (5)", out)
	}
}

// TestEditorCommandSupportsPlainSingleWordEditor confirms the common
// case (EDITOR="vi", EDITOR="nano", or unset) still works unchanged
// through the shell wrapper.
func TestEditorCommandSupportsPlainSingleWordEditor(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := editorCommand("cat", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd.CombinedOutput: %v: %s", err, out)
	}
	if string(out) != "hello" {
		t.Errorf("output = %q, want %q", out, "hello")
	}
}
