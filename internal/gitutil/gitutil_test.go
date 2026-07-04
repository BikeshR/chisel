package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a real git repo in a temp dir, with local (not global)
// user config so the test doesn't depend on the machine's git setup.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "chisel-test")
	run("config", "user.email", "chisel-test@example.com")
	return dir
}

func TestIsRepo(t *testing.T) {
	if !IsRepo(initRepo(t)) {
		t.Error("IsRepo = false for a real git repo")
	}
	if IsRepo(t.TempDir()) {
		t.Error("IsRepo = true for a plain directory")
	}
}

func TestDirtyPaths(t *testing.T) {
	dir := initRepo(t)

	paths, err := DirtyPaths(dir)
	if err != nil {
		t.Fatalf("DirtyPaths: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want empty for a fresh repo", paths)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err = DirtyPaths(dir)
	if err != nil {
		t.Fatalf("DirtyPaths: %v", err)
	}
	if !paths["a.txt"] {
		t.Errorf("paths = %v, want a.txt present", paths)
	}
}

// TestDirtyPathsHandlesUnicodeAndSpecialFilenames is the regression
// test for why DirtyPaths uses `git status --porcelain -z`: without -z,
// git C-quotes non-ASCII and otherwise "unusual" filenames in the
// plain-text porcelain output, and the old strings.Trim(path, `"`)
// unwrapped only the surrounding quotes, not the escaping inside them —
// a name with a literal quote or backslash would come out wrong.
func TestDirtyPathsHandlesUnicodeAndSpecialFilenames(t *testing.T) {
	dir := initRepo(t)

	names := []string{"café.txt", `has"quote.txt`, "emoji😀.txt"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := DirtyPaths(dir)
	if err != nil {
		t.Fatalf("DirtyPaths: %v", err)
	}
	for _, name := range names {
		if !paths[name] {
			t.Errorf("paths = %v, want %q present exactly as written", paths, name)
		}
	}
}

// TestDirtyPathsHandlesRenames confirms the new -z-based rename
// handling (new path, then a NUL-separated original path field to skip
// over) reports the renamed file under its new name, not the old one
// and not the old-path field misread as a second, bogus entry.
func TestDirtyPathsHandlesRenames(t *testing.T) {
	dir := initRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("add", "old.txt")
	run("commit", "-m", "add old.txt")
	run("mv", "old.txt", "new.txt")

	paths, err := DirtyPaths(dir)
	if err != nil {
		t.Fatalf("DirtyPaths: %v", err)
	}
	if !paths["new.txt"] {
		t.Errorf("paths = %v, want new.txt present", paths)
	}
	if paths["old.txt"] {
		t.Errorf("paths = %v, want old.txt absent (that's the original-path field, not a real dirty path)", paths)
	}
}

func TestCommitNewlyChanged(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha, err := CommitNewlyChanged(dir, map[string]bool{}, "chisel: add a.txt")
	if err != nil {
		t.Fatalf("CommitNewlyChanged: %v", err)
	}
	if sha == "" {
		t.Error("CommitNewlyChanged returned an empty SHA")
	}

	paths, err := DirtyPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want empty right after a commit", paths)
	}

	log := exec.Command("git", "log", "-1", "--format=%s")
	log.Dir = dir
	out, err := log.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "chisel: add a.txt\n" {
		t.Errorf("commit message = %q, want %q", got, "chisel: add a.txt\n")
	}
}

func TestCommitNewlyChangedNothingNew(t *testing.T) {
	dir := initRepo(t)
	sha, err := CommitNewlyChanged(dir, map[string]bool{}, "empty commit attempt")
	if err != nil {
		t.Fatalf("CommitNewlyChanged: %v", err)
	}
	if sha != "" {
		t.Errorf("sha = %q, want empty when there's nothing to commit", sha)
	}
}

// TestCommitNewlyChangedExcludesPreexistingDirtyFiles is the direct
// regression test for the bug this function replaced CommitAll (which
// ran `git add -A`) to fix: auto-commit must never sweep up the user's
// own unrelated, already-unstaged work sitting in the same working tree.
func TestCommitNewlyChangedExcludesPreexistingDirtyFiles(t *testing.T) {
	dir := initRepo(t)

	// The user's own in-progress work, already dirty before this "turn"
	// started — chisel had nothing to do with it.
	if err := os.WriteFile(filepath.Join(dir, "user-wip.txt"), []byte("user's own edits"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := DirtyPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !before["user-wip.txt"] {
		t.Fatalf("before = %v, want user-wip.txt present as the pre-existing baseline", before)
	}

	// Now chisel makes its own change during the turn.
	if err := os.WriteFile(filepath.Join(dir, "chisel-edit.txt"), []byte("chisel's edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha, err := CommitNewlyChanged(dir, before, "chisel: add chisel-edit.txt")
	if err != nil {
		t.Fatalf("CommitNewlyChanged: %v", err)
	}
	if sha == "" {
		t.Fatal("expected a real commit for the newly-added file")
	}

	// The user's file must still be sitting there dirty, uncommitted.
	after, err := DirtyPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !after["user-wip.txt"] {
		t.Error("user-wip.txt was swept into the commit — it should have stayed untouched and still dirty")
	}
	if after["chisel-edit.txt"] {
		t.Error("chisel-edit.txt is still dirty — it should have been committed")
	}

	show := exec.Command("git", "show", "--name-only", "--format=", "HEAD")
	show.Dir = dir
	out, err := show.Output()
	if err != nil {
		t.Fatal(err)
	}
	committed := string(out)
	if !strings.Contains(committed, "chisel-edit.txt") {
		t.Errorf("committed files = %q, want chisel-edit.txt", committed)
	}
	if strings.Contains(committed, "user-wip.txt") {
		t.Errorf("committed files = %q, want user-wip.txt excluded", committed)
	}
}
