// Package gitutil provides the small slice of git plumbing chisel's
// optional auto-commit feature needs. Nothing here assumes dir is a git
// repository — every function reports that rather than erroring
// confusingly if it isn't.
package gitutil

import (
	"fmt"
	"os/exec"
	"strings"
)

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// HasChanges reports whether dir has any uncommitted changes, staged or
// not, tracked or not.
func HasChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// CommitAll stages every change in dir and commits it with message,
// returning the new commit's short SHA.
func CommitAll(dir, message string) (sha string, err error) {
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, out)
	}

	commit := exec.Command("git", "commit", "-m", message)
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, out)
	}

	rev := exec.Command("git", "rev-parse", "--short", "HEAD")
	rev.Dir = dir
	out, err := rev.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
