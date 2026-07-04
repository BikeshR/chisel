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
