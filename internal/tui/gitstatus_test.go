package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRefreshGitStatusNotARepo(t *testing.T) {
	msg := refreshGitStatus(t.TempDir())()
	got, ok := msg.(gitStatusMsg)
	if !ok {
		t.Fatalf("got %T, want gitStatusMsg", msg)
	}
	if got.isRepo {
		t.Error("isRepo = true for a plain directory")
	}
}

func TestRefreshGitStatusCleanRepo(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "initial")

	msg := refreshGitStatus(dir)()
	got, ok := msg.(gitStatusMsg)
	if !ok {
		t.Fatalf("got %T, want gitStatusMsg", msg)
	}
	if !got.isRepo {
		t.Error("isRepo = false for a real repo")
	}
	if got.branch == "" {
		t.Error("branch is empty, want a real branch name")
	}
	if got.dirty {
		t.Error("dirty = true right after a clean commit")
	}

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg = refreshGitStatus(dir)()
	got = msg.(gitStatusMsg)
	if !got.dirty {
		t.Error("dirty = false after adding an untracked file")
	}
}

func TestUpdateAppliesGitStatusMsg(t *testing.T) {
	m := Model{}
	got, cmd := m.Update(gitStatusMsg{isRepo: true, branch: "feature-x", dirty: true})
	if cmd != nil {
		t.Error("expected a nil Cmd from applying a gitStatusMsg")
	}
	gotModel := got.(Model)
	if !gotModel.gitIsRepo || gotModel.gitBranch != "feature-x" || !gotModel.gitDirty {
		t.Errorf("got isRepo=%v branch=%q dirty=%v, want true/\"feature-x\"/true", gotModel.gitIsRepo, gotModel.gitBranch, gotModel.gitDirty)
	}
}
