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

// Branch returns dir's current git branch name, and whether one could
// actually be resolved — false for a detached HEAD (rev-parse reports
// the literal string "HEAD" then, not a real branch name) or dir not
// being a repo at all, rather than surfacing either as a confusing
// "branch" of "HEAD".
func Branch(dir string) (name string, ok bool) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	name = strings.TrimSpace(string(out))
	if name == "" || name == "HEAD" {
		return "", false
	}
	return name, true
}

// DirtyPaths returns the set of paths git considers changed in dir —
// staged or not, tracked or not — as they appear in `git status
// --porcelain -z` (relative to dir). The -z form is what makes this
// correct for any filename: without it, git C-quotes "unusual"
// characters (non-ASCII, spaces, quotes themselves) in a shell-escaped
// form that a plain strings.Trim(path, `"`) only half-undoes — a
// filename with, say, a literal backslash or an actual embedded quote
// would come out wrong, and the later `git add --` on that mangled
// string would then fail. -z instead prints paths as raw, unquoted
// bytes, NUL-separated.
func DirtyPaths(dir string) (map[string]bool, error) {
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	paths := map[string]bool{}
	entries := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		// Porcelain -z format is "XY path" per entry, NUL-terminated —
		// the status codes are exactly 2 characters, then a space, then
		// the raw path. A rename or copy (R or C in either status
		// position) adds one more NUL-separated field right after this
		// one carrying the *original* path — skip over it rather than
		// misreading it as its own entry.
		status, path := entry[:2], entry[3:]
		if strings.ContainsAny(status, "RC") {
			i++
		}
		paths[path] = true
	}
	return paths, nil
}

// missingGitIdentityArgs returns "-c user.name=..."/"-c user.email=..."
// pairs for whichever of the two dir's git config doesn't already
// resolve (checking local config, then global, the same cascade `git
// config` itself uses) — empty if both are already set, so a user with
// a real identity configured never has it silently replaced.
func missingGitIdentityArgs(dir string) []string {
	var args []string
	if !gitConfigResolves(dir, "user.name") {
		args = append(args, "-c", "user.name=chisel")
	}
	if !gitConfigResolves(dir, "user.email") {
		args = append(args, "-c", "user.email=chisel@localhost")
	}
	return args
}

func gitConfigResolves(dir, key string) bool {
	cmd := exec.Command("git", "config", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// CommitNewlyChanged stages only the paths that are dirty now but
// weren't in before, and commits them with message — before is typically
// a DirtyPaths snapshot taken right when a turn started. Committing only
// the diff against that snapshot, rather than every currently-dirty path
// (`git add -A` would), is what keeps auto-commit from sweeping up
// whatever unrelated, unstaged work the user already had sitting in the
// same working tree before chisel touched anything. Returns "" (no
// error) if nothing new turned up to commit.
func CommitNewlyChanged(dir string, before map[string]bool, message string) (sha string, err error) {
	after, err := DirtyPaths(dir)
	if err != nil {
		return "", err
	}

	var newPaths []string
	for p := range after {
		if !before[p] {
			newPaths = append(newPaths, p)
		}
	}
	if len(newPaths) == 0 {
		return "", nil
	}

	add := exec.Command("git", append([]string{"add", "--"}, newPaths...)...)
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, out)
	}

	// Fills in only whatever piece of the identity is actually unset —
	// unlike internal/checkpoint's shadow repo (chisel's own private
	// thing, always committed as "chisel"), this is the user's real
	// repository, so an identity they DO have configured (local or
	// global) is never overridden, just backstopped against git's
	// otherwise-hard "Please tell me who you are" failure for someone
	// who simply never set one up. -c is process-local, not written to
	// the repo's own config.
	args := append(missingGitIdentityArgs(dir), "commit", "-m", message)
	commit := exec.Command("git", args...)
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
