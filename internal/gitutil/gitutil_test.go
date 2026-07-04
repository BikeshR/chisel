package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestHasChanges(t *testing.T) {
	dir := initRepo(t)

	has, err := HasChanges(dir)
	if err != nil {
		t.Fatalf("HasChanges: %v", err)
	}
	if has {
		t.Error("HasChanges = true for an empty repo with nothing written")
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	has, err = HasChanges(dir)
	if err != nil {
		t.Fatalf("HasChanges: %v", err)
	}
	if !has {
		t.Error("HasChanges = false after writing an untracked file")
	}
}

func TestCommitAll(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha, err := CommitAll(dir, "chisel: add a.txt")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if sha == "" {
		t.Error("CommitAll returned an empty SHA")
	}

	has, err := HasChanges(dir)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("HasChanges = true right after a commit, want everything clean")
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

func TestCommitAllNothingToCommit(t *testing.T) {
	dir := initRepo(t)
	if _, err := CommitAll(dir, "empty commit attempt"); err == nil {
		t.Error("CommitAll with nothing changed should fail, not silently succeed")
	}
}
