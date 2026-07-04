package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	s, err := Open(workDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, workDir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()

	s1, err := Open(workDir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s2, err := Open(workDir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if s1.gitDir != s2.gitDir {
		t.Errorf("gitDir differs across opens: %q vs %q", s1.gitDir, s2.gitDir)
	}
}

func TestCheckpointCreatesRestorableSnapshot(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "version 1")

	hash, err := s.Checkpoint("first checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if hash == "" {
		t.Fatal("expected a non-empty hash")
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Hash != hash || entries[0].Label != "first checkpoint" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestCheckpointReusesUnchangedState(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "content")

	hash1, err := s.Checkpoint("first")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	hash2, err := s.Checkpoint("second, but nothing changed")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("expected the same checkpoint reused when nothing changed, got %q then %q", hash1, hash2)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries, want 1 (no new commit for an unchanged state)", len(entries))
	}
}

func TestCheckpointCreatesNewCommitOnChange(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "version 1")
	hash1, err := s.Checkpoint("first")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	writeFile(t, workDir, "a.txt", "version 2")
	hash2, err := s.Checkpoint("second")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if hash1 == hash2 {
		t.Error("expected a new commit hash after a real file change")
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Most recent first.
	if entries[0].Hash != hash2 || entries[1].Hash != hash1 {
		t.Errorf("entries = %+v, want most-recent-first ordering", entries)
	}
}

func TestRestoreRevertsFileContent(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "original")
	hash1, err := s.Checkpoint("original state")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	writeFile(t, workDir, "a.txt", "modified")
	if _, err := s.Checkpoint("modified state"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if err := s.Restore(hash1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("a.txt = %q, want %q after restore", got, "original")
	}
}

func TestRestoreRemovesFilesCreatedAfterCheckpoint(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "content")
	hash1, err := s.Checkpoint("before new file")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	writeFile(t, workDir, "b.txt", "a new file")
	if _, err := s.Checkpoint("after new file"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if err := s.Restore(hash1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("b.txt should have been removed by restore, stat err = %v", err)
	}
}

func TestRestoreDoesNotDestroyHistory(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "v1")
	hash1, err := s.Checkpoint("v1")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	writeFile(t, workDir, "a.txt", "v2")
	hash2, err := s.Checkpoint("v2")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if err := s.Restore(hash1); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// hash2's commit object should still exist and be checkable out
	// directly, even though it's no longer on the current line of
	// history — Restore's own safety-net checkpoint plus git's
	// append-only commit storage means nothing was actually destroyed.
	if err := s.Restore(hash2); err != nil {
		t.Fatalf("Restore back to hash2: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Errorf("a.txt = %q, want %q — v2's commit should still be reachable by hash", got, "v2")
	}
}

func TestCheckpointExcludesDotGitAndDotChisel(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "content")
	if err := os.MkdirAll(filepath.Join(workDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workDir, ".git"), "HEAD", "ref: refs/heads/main")
	if err := os.MkdirAll(filepath.Join(workDir, ".chisel"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workDir, ".chisel"), "hooks.json", "{}")

	if _, err := s.Checkpoint("with dotdirs present"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	out, err := s.git("show", "--stat", "--format=", "HEAD")
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	for _, bad := range []string{".git/", ".chisel/"} {
		if strings.Contains(out, bad) {
			t.Errorf("checkpoint tracked %q, want it excluded: %s", bad, out)
		}
	}
}

func TestListEmptyBeforeAnyCheckpoint(t *testing.T) {
	s, _ := newTestStore(t)
	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want empty before any checkpoint", entries)
	}
}

func TestCheckpointEmptyLabelFallsBack(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "content")
	if _, err := s.Checkpoint(""); err != nil {
		t.Fatalf("Checkpoint with empty label: %v", err)
	}
}
