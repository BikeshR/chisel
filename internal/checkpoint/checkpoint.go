// Package checkpoint snapshots a project's working-directory file state
// before each turn into a shadow git repository — kept entirely outside
// the project, under ~/.chisel/checkpoints/<hash>/, so it never
// interferes with the project's own git history, its .gitignore-based
// tooling, or /git auto's own commits. internal/tui's /rewind command
// uses this to restore file state to an earlier point; the mapping
// from a checkpoint hash back to a point in the conversation lives in
// the tui package, not here — this package only knows about files.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Store manages one project's shadow git repository.
type Store struct {
	gitDir  string // the shadow repo's --git-dir
	workDir string // the real project directory, used as --work-tree
}

// Entry is one checkpoint in the shadow repo's history.
type Entry struct {
	Hash  string
	Label string
	When  time.Time
}

// Open prepares the shadow repository for workDir, creating it on first
// use. Safe to call every time chisel starts.
func Open(workDir string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	s := &Store{
		gitDir:  filepath.Join(home, ".chisel", "checkpoints", sanitize(workDir), "repo.git"),
		workDir: workDir,
	}

	if _, err := os.Stat(s.gitDir); err == nil {
		// Best-effort: pruning failing shouldn't make an otherwise-working
		// checkpoint store unavailable — worst case, history just isn't
		// pruned this run, exactly the pre-existing behavior.
		_ = s.Prune(maxCheckpointHistory)
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.gitDir), 0o700); err != nil {
		return nil, err
	}
	if _, err := s.git("init", "--quiet"); err != nil {
		return nil, fmt.Errorf("init checkpoint repo: %w", err)
	}
	// Local identity so a commit never depends on the user's own global
	// git config (or lack of one) being present.
	if _, err := s.git("config", "user.name", "chisel"); err != nil {
		return nil, err
	}
	if _, err := s.git("config", "user.email", "chisel@localhost"); err != nil {
		return nil, err
	}
	return s, nil
}

// git runs git against this store's shadow --git-dir with --work-tree
// pointed at the real project directory — the standard technique for a
// git repo whose metadata lives separately from what it tracks.
func (s *Store) git(args ...string) (string, error) {
	full := append([]string{"--git-dir=" + s.gitDir, "--work-tree=" + s.workDir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (s *Store) hasCommits() bool {
	_, err := s.git("rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// Checkpoint commits the current state of workDir's files into the
// shadow repo, labeled with label (typically the user message about to
// run). .gitignore is honored automatically — it's read from the
// work-tree regardless of which --git-dir is active — and the
// project's own .git and chisel's own .chisel are always excluded, on
// top of whatever the project's .gitignore already covers. If nothing
// changed since the last checkpoint, the existing one is reused rather
// than creating a pointless empty commit.
func (s *Store) Checkpoint(label string) (hash string, err error) {
	if label == "" {
		label = "checkpoint"
	}

	if _, err := s.git("add", "-A", "--", ".", ":!.git", ":!.chisel"); err != nil {
		return "", fmt.Errorf("stage files: %w", err)
	}

	if s.hasCommits() {
		if out, _ := s.git("status", "--porcelain"); strings.TrimSpace(out) == "" {
			if head, err := s.git("rev-parse", "HEAD"); err == nil {
				return strings.TrimSpace(head), nil
			}
		}
	}

	if _, err := s.git("commit", "--quiet", "--allow-empty", "-m", label); err != nil {
		return "", fmt.Errorf("commit checkpoint: %w", err)
	}
	out, err := s.git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// List returns every checkpoint reachable from the current state, most
// recent first. Empty, not an error, if nothing has been checkpointed
// yet.
func (s *Store) List() ([]Entry, error) {
	if !s.hasCommits() {
		return nil, nil
	}

	// %x00 as the field separator avoids any ambiguity from a label
	// that happens to contain a more usual choice (a colon, a pipe).
	// %H full commit hash, %ct commit time (unix seconds), %s subject
	// (chisel always passes the label as the whole commit message, so
	// %s is exactly that label's first line).
	out, err := s.git("log", "--format=%H%x00%ct%x00%s")
	if err != nil {
		return nil, err
	}

	var entries []Entry
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		sec, _ := strconv.ParseInt(parts[1], 10, 64)
		entries = append(entries, Entry{Hash: parts[0], Label: parts[2], When: time.Unix(sec, 0)})
	}
	return entries, nil
}

// Restore checks out hash's file tree into workDir, overwriting current
// file content — destructive, callers must confirm with the user
// first. The current state is checkpointed first (labeled "before
// rewind"), so nothing is ever actually destroyed: the shadow repo's
// history is append-only, restoring an earlier point just moves what
// the working tree currently reflects, not what's recorded.
func (s *Store) Restore(hash string) error {
	if _, err := s.Checkpoint("before rewind"); err != nil {
		return fmt.Errorf("snapshot current state before restoring: %w", err)
	}
	// reset --hard makes tracked files exactly match hash — updating
	// changed ones and removing ones that don't exist there — without
	// touching .git/.chisel, since neither was ever tracked in this
	// repo in the first place (see Checkpoint's pathspec excludes).
	if _, err := s.git("reset", "--hard", hash); err != nil {
		return fmt.Errorf("restore checkpoint: %w", err)
	}
	// reset --hard only affects tracked files — a file created (and
	// never checkpointed) since the target commit would otherwise
	// survive as an untracked leftover.
	if _, err := s.git("clean", "-fd", "--", ".", ":!.git", ":!.chisel"); err != nil {
		return fmt.Errorf("clean up files newer than the checkpoint: %w", err)
	}
	return nil
}

// maxCheckpointHistory bounds how many commits the shadow repo keeps —
// without this, a long-running daily-driver project accumulates one
// checkpoint commit per turn forever, growing the shadow repo's on-disk
// size indefinitely. Pruned history is folded into a single synthetic
// root commit (see Prune) rather than deleted outright, so every commit
// within the kept window survives untouched, same hash and all; only
// /rewind targets older than that window stop being reachable.
const maxCheckpointHistory = 500

// Prune folds every commit older than the most recent keep into a
// rewritten history rooted at a synthetic commit carrying the same
// tree/message/time as the oldest kept entry — dropping everything
// before it from reachable history, without touching workDir at all (a
// no-op on the actual checked-out files, purely a rewrite of the shadow
// branch's ref). A no-op if there are keep or fewer commits already.
//
// Every kept commit's tree/message/timestamp survives identically, but
// — unavoidably, since a commit's hash is computed over its parent's
// hash too — every one of them gets a *new* hash, not just the ones
// actually folded away: rewriting the oldest kept commit's ancestry
// changes its hash, which changes its child's hash, and so on all the
// way to the tip. That's exactly why chisel only ever calls this once,
// from Open, at startup, rather than at arbitrary points: /rewind's
// in-memory checkpoint records (internal/tui) are never reloaded from
// git history on resume, so they only ever reference checkpoints taken
// *this* process, which are always created after this runs — nothing
// live ever holds a reference to a hash Prune could invalidate.
func (s *Store) Prune(keep int) error {
	entries, err := s.List() // most-recent-first
	if err != nil || len(entries) <= keep {
		return err
	}

	branchRef, err := s.git("symbolic-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve current branch: %w", err)
	}
	branchRef = strings.TrimSpace(branchRef)

	kept := entries[:keep] // most-recent-first; the last one is the oldest to preserve

	root := kept[len(kept)-1]
	tree, err := s.treeOf(root.Hash)
	if err != nil {
		return err
	}
	newHash, err := s.commitTree(tree, "", root.Label, root.When)
	if err != nil {
		return err
	}

	// Walk the rest oldest-to-newest (kept is newest-first), recreating
	// each on top of the new, shorter chain so every one of them keeps
	// its original tree/message/time — only its ancestry actually changes.
	for i := len(kept) - 2; i >= 0; i-- {
		e := kept[i]
		tree, err := s.treeOf(e.Hash)
		if err != nil {
			return err
		}
		newHash, err = s.commitTree(tree, newHash, e.Label, e.When)
		if err != nil {
			return err
		}
	}

	if _, err := s.git("update-ref", branchRef, newHash); err != nil {
		return fmt.Errorf("update branch after pruning: %w", err)
	}
	return nil
}

// treeOf resolves hash's tree object, for recreating a commit with the
// same content but a different parent (see Prune).
func (s *Store) treeOf(hash string) (string, error) {
	out, err := s.git("rev-parse", hash+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve tree for %s: %w", hash, err)
	}
	return strings.TrimSpace(out), nil
}

// commitTree creates a new commit object for tree with the given parent
// (or no parent at all, for a synthetic root, when parent == ""),
// message, and author/committer time — the plumbing Prune uses to
// recreate a kept commit's exact content under a shorter ancestor chain.
func (s *Store) commitTree(tree, parent, message string, when time.Time) (string, error) {
	args := []string{"--git-dir=" + s.gitDir, "--work-tree=" + s.workDir, "commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}

	cmd := exec.Command("git", args...)
	ts := strconv.FormatInt(when.Unix(), 10) + " +0000"
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=chisel", "GIT_AUTHOR_EMAIL=chisel@localhost", "GIT_AUTHOR_DATE="+ts,
		"GIT_COMMITTER_NAME=chisel", "GIT_COMMITTER_EMAIL=chisel@localhost", "GIT_COMMITTER_DATE="+ts,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("commit-tree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// staleCheckpointRepoAge bounds how long a project's entire checkpoint
// shadow repository is kept once its most recent commit is older than
// this — the same reasoning session.PruneOld's own staleness window
// applies (there's no way to tell whether the project directory a
// checkpoint repo belongs to still exists, since sanitize is one-way,
// so age is what decides instead). Unlike Prune (which trims one
// already-open project's own history down to a commit count), this
// removes the whole repo: these hold full file-tree snapshots, easily
// the largest disk footprint under ~/.chisel, and an abandoned
// project's would otherwise sit there forever.
const staleCheckpointRepoAge = 90 * 24 * time.Hour

// PruneStaleRepos removes whole shadow repositories under
// ~/.chisel/checkpoints whose most recent commit is older than
// staleCheckpointRepoAge. Best-effort throughout: a missing checkpoints
// root isn't an error, and one repo that can't be read or removed
// doesn't stop the rest from being checked.
//
// Call before Open for the current project, not after (the opposite
// order from session.PruneOld): if the current project's own repo
// happens to be the stale one — say, a project untouched for months
// just got reopened — Open's own "recreate if missing" branch handles
// that fine, but only if it's the one doing the creating. Pruning it
// out from under an already-open Store would leave that Store's gitDir
// pointing at nothing, and git has no equivalent self-healing for a
// --git-dir that vanished mid-session.
func PruneStaleRepos() (removed int, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	root := filepath.Join(home, ".chisel", "checkpoints")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := time.Now().Add(-staleCheckpointRepoAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		when, ok := lastCommitTime(filepath.Join(dir, "repo.git"))
		if !ok || !when.Before(cutoff) {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed++
		}
	}
	return removed, nil
}

// lastCommitTime returns the most recent commit's timestamp in the
// shadow repo at gitDir, and whether one could be determined at all —
// false for a repo with no commits yet, or one that's missing/corrupt.
func lastCommitTime(gitDir string) (time.Time, bool) {
	out, err := exec.Command("git", "--git-dir="+gitDir, "log", "-1", "--format=%ct").Output()
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// sanitize turns workDir into a filesystem-safe, collision-resistant
// directory name — the same approach as internal/session's sanitize: a
// readable prefix for a human skimming ~/.chisel/checkpoints, plus a
// hash suffix that's what actually guarantees two different paths
// never collide.
func sanitize(workDir string) string {
	s := strings.TrimPrefix(filepath.ToSlash(workDir), "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		s = "root"
	}
	sum := sha256.Sum256([]byte(workDir))
	return s + "-" + hex.EncodeToString(sum[:4])
}
