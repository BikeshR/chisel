package agentmemory

import (
	"os"
	"strings"
	"testing"
)

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	content, found := Load(t.TempDir())
	if found || content != "" {
		t.Errorf("Load = %q, %v, want empty, false for a missing file", content, found)
	}
}

func TestRememberThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "this repo uses tabs not spaces"); err != nil {
		t.Fatal(err)
	}

	content, found := Load(dir)
	if !found {
		t.Fatal("found = false, want true after Remember")
	}
	if !strings.Contains(content, "this repo uses tabs not spaces") {
		t.Errorf("content = %q, want the remembered note present", content)
	}
}

func TestRememberAccumulatesMultipleNotes(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "first note"); err != nil {
		t.Fatal(err)
	}
	if err := Remember(dir, "second note"); err != nil {
		t.Fatal(err)
	}

	content, _ := Load(dir)
	if !strings.Contains(content, "first note") || !strings.Contains(content, "second note") {
		t.Errorf("content = %q, want both notes present", content)
	}
}

func TestRememberFlattensNewlinesToOneLine(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "line one\nline two\n\nline three"); err != nil {
		t.Fatal(err)
	}

	content, _ := Load(dir)
	lines := strings.Split(content, "\n")
	if len(lines) != 1 {
		t.Errorf("content = %q, want exactly one line — embedded newlines should be flattened to spaces", content)
	}
	if !strings.Contains(content, "line one line two line three") {
		t.Errorf("content = %q, want the flattened single-line note", content)
	}
}

func TestRememberRejectsEmptyNote(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "   \n  "); err == nil {
		t.Error("expected an error for a whitespace-only note")
	}
}

// TestRememberCapsSizeByDroppingOldestEntries is the direct test of the
// size-cap discipline maxBytes exists for: without it, a long-running
// project's MEMORY.md would grow unbounded across many sessions, adding
// an ever-larger, ever-slower system prompt to every future request.
func TestRememberCapsSizeByDroppingOldestEntries(t *testing.T) {
	dir := t.TempDir()
	// Each note is ~50 bytes; comfortably over maxBytes (25_000) after
	// enough of them, forcing the oldest ones out.
	note := strings.Repeat("x", 50)
	for i := 0; i < 700; i++ {
		if err := Remember(dir, note); err != nil {
			t.Fatalf("Remember #%d: %v", i, err)
		}
	}

	data, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxBytes {
		t.Errorf("MEMORY.md size = %d bytes, want capped at or under %d", len(data), maxBytes)
	}
	if len(data) == 0 {
		t.Error("MEMORY.md is empty, want at least the most recent notes retained")
	}
}

func TestClearRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "something"); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir); err != nil {
		t.Fatal(err)
	}

	if _, found := Load(dir); found {
		t.Error("found = true after Clear, want false")
	}
}

func TestClearOnMissingFileIsNotAnError(t *testing.T) {
	if err := Clear(t.TempDir()); err != nil {
		t.Errorf("Clear on a missing file returned %v, want nil", err)
	}
}

// TestRememberWritesAtomicallyNoLeftoverTempFile mirrors the same
// regression test permrules and session already have for their own
// writeAtomic — a crash mid-write must never leave a stray temp file
// behind in .chisel/.
func TestRememberWritesAtomicallyNoLeftoverTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := Remember(dir, "a note"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir + "/.chisel")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q found in .chisel", e.Name())
		}
	}
}
