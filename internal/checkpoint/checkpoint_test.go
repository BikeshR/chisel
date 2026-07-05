package checkpoint

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestPruneKeepsMostRecentAndFoldsOlderHistory(t *testing.T) {
	s, workDir := newTestStore(t)

	var hashes []string
	for i := 0; i < 10; i++ {
		writeFile(t, workDir, "a.txt", strconv.Itoa(i))
		hash, err := s.Checkpoint("v" + strconv.Itoa(i))
		if err != nil {
			t.Fatalf("Checkpoint %d: %v", i, err)
		}
		hashes = append(hashes, hash)
	}

	if err := s.Prune(3); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries after pruning to 3, want 3: %+v", len(entries), entries)
	}
	// The 3 most recent commits keep their labels/content, but NOT their
	// original hashes — a commit's hash is computed over its parent's
	// hash too, so rewriting the ancestry chain unavoidably changes every
	// descendant's hash, not just the ones actually folded away (see
	// Prune's own doc comment). The old hashes are gone from this repo
	// entirely now, so re-asserting them here isn't meaningful — what
	// matters is that content/order survived.
	wantLabels := []string{"v9", "v8", "v7"}
	for i, e := range entries {
		if e.Label != wantLabels[i] {
			t.Errorf("entries[%d].Label = %q, want %q", i, e.Label, wantLabels[i])
		}
		if e.Hash == hashes[9-i] {
			t.Errorf("entries[%d].Hash = %q, expected pruning to change it (ancestry was rewritten)", i, e.Hash)
		}
	}

	// The most recent kept commit must still be restorable under its NEW
	// (post-prune) hash — pruning must not corrupt the actual content.
	if err := s.Restore(entries[0].Hash); err != nil {
		t.Fatalf("Restore to most recent commit after prune: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "9" {
		t.Errorf("a.txt = %q, want %q", got, "9")
	}
}

func TestPruneNoOpWhenUnderLimit(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "v1")
	hash, err := s.Checkpoint("only checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	if err := s.Prune(500); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Hash != hash {
		t.Errorf("entries = %+v, want the single original checkpoint untouched", entries)
	}
}

func TestOpenPrunesExistingLongHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	s, err := Open(workDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < maxCheckpointHistory+50; i++ {
		writeFile(t, workDir, "a.txt", strconv.Itoa(i))
		if _, err := s.Checkpoint("v" + strconv.Itoa(i)); err != nil {
			t.Fatalf("Checkpoint %d: %v", i, err)
		}
	}

	// Re-opening the same store (as chisel does on every startup) should
	// prune it back down to the cap.
	s2, err := Open(workDir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	entries, err := s2.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != maxCheckpointHistory {
		t.Errorf("got %d entries after re-opening, want exactly maxCheckpointHistory (%d)", len(entries), maxCheckpointHistory)
	}
}

func TestCheckpointEmptyLabelFallsBack(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "content")
	if _, err := s.Checkpoint(""); err != nil {
		t.Fatalf("Checkpoint with empty label: %v", err)
	}
}

func TestDiffShowsChangesSinceCheckpoint(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "version 1\n")
	hash, err := s.Checkpoint("first checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	writeFile(t, workDir, "a.txt", "version 2\n")

	diff, err := s.Diff(hash)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "-version 1") || !strings.Contains(diff, "+version 2") {
		t.Errorf("diff = %q, want it to show the actual content change", diff)
	}
}

func TestDiffEmptyWhenNothingChanged(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "unchanged content\n")
	hash, err := s.Checkpoint("checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	diff, err := s.Diff(hash)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.TrimSpace(diff) != "" {
		t.Errorf("diff = %q, want empty when nothing changed since the checkpoint", diff)
	}
}

// TestDiffDoesNotModifyFiles confirms Diff is read-only — unlike
// Restore, which is documented as destructive, Diff must never touch
// the working tree.
func TestDiffDoesNotModifyFiles(t *testing.T) {
	s, workDir := newTestStore(t)
	writeFile(t, workDir, "a.txt", "version 1\n")
	hash, err := s.Checkpoint("first checkpoint")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	writeFile(t, workDir, "a.txt", "version 2\n")

	if _, err := s.Diff(hash); err != nil {
		t.Fatalf("Diff: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "version 2\n" {
		t.Errorf("a.txt = %q, want it unchanged by Diff", string(data))
	}
}

// TestPruneStaleReposRemovesOldRepoNotFreshOne is the regression test
// for a real disk-usage gap: sessions get age-based pruning and a
// project's own checkpoint history is capped by commit count, but a
// whole abandoned project's shadow repo — full file-tree snapshots, the
// largest thing under ~/.chisel — used to persist forever.
func TestPruneStaleReposRemovesOldRepoNotFreshOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	staleWorkDir := t.TempDir()
	staleStore, err := Open(staleWorkDir)
	if err != nil {
		t.Fatalf("Open (stale): %v", err)
	}
	hash, err := staleStore.Checkpoint("only commit")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	// Backdate the commit well past staleCheckpointRepoAge, recreating
	// it at an old timestamp the same way Prune itself rewrites history.
	tree, err := staleStore.treeOf(hash)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleCheckpointRepoAge - 24*time.Hour)
	newHash, err := staleStore.commitTree(tree, "", "only commit", old)
	if err != nil {
		t.Fatal(err)
	}
	branchRef, err := staleStore.git("symbolic-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staleStore.git("update-ref", strings.TrimSpace(branchRef), newHash); err != nil {
		t.Fatal(err)
	}

	freshWorkDir := t.TempDir()
	freshStore, err := Open(freshWorkDir)
	if err != nil {
		t.Fatalf("Open (fresh): %v", err)
	}
	if _, err := freshStore.Checkpoint("recent commit"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	removed, err := PruneStaleRepos()
	if err != nil {
		t.Fatalf("PruneStaleRepos: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	if _, err := os.Stat(staleStore.gitDir); !os.IsNotExist(err) {
		t.Error("expected the stale repo's directory to be removed")
	}
	if _, err := os.Stat(freshStore.gitDir); err != nil {
		t.Errorf("expected the fresh repo's directory to survive: %v", err)
	}
}

func TestPruneStaleReposNoCheckpointsRootIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	removed, err := PruneStaleRepos()
	if err != nil {
		t.Fatalf("PruneStaleRepos: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// TestPruneStaleReposLeavesRepoWithNoCommitsAlone confirms a repo whose
// age can't be determined (never checkpointed) is left alone rather
// than guessed at — lastCommitTime returning ok=false must not be
// treated as "infinitely old."
func TestPruneStaleReposLeavesRepoWithNoCommitsAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if _, err := Open(workDir); err != nil {
		t.Fatalf("Open: %v", err)
	}

	removed, err := PruneStaleRepos()
	if err != nil {
		t.Fatalf("PruneStaleRepos: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 — a never-checkpointed repo shouldn't be pruned by guesswork", removed)
	}
}
