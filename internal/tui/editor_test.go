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
