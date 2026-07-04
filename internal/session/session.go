// Package session persists chisel conversation history to disk, scoped by
// working directory, so closing and reopening chisel in the same project
// picks the conversation back up instead of starting cold every time.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

type file struct {
	SavedAt  time.Time       `json:"saved_at"`
	Messages []agent.Message `json:"messages"`
}

// Path returns the session file for workDir. Sessions are scoped per
// directory rather than kept globally — chisel is used across many
// projects, and each should resume its own conversation independently.
func Path(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "sessions", sanitize(workDir)+".json"), nil
}

// sanitize turns an absolute path into a single filesystem-safe component,
// readable enough to eyeball in `ls ~/.chisel/sessions` — e.g.
// /home/brana/code/chisel becomes home-brana-code-chisel-3f2a91c0. The
// trailing hash (of the untouched workDir, not the readable part) is
// what actually makes the name unique — without it, distinct directories
// like /home/x/a-b and /home/x/a/b both collapse to home-x-a-b and
// would silently share (and overwrite) one session file.
func sanitize(workDir string) string {
	s := strings.TrimPrefix(filepath.ToSlash(workDir), "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		s = "root"
	}
	sum := sha256.Sum256([]byte(workDir))
	return s + "-" + hex.EncodeToString(sum[:4])
}

// Load returns the saved messages for workDir and when they were saved.
// ok is false whether there's simply no saved session or the file exists
// but couldn't be read or parsed — either way that's not fatal, callers
// should just proceed with a fresh session.
func Load(workDir string) (messages []agent.Message, savedAt time.Time, ok bool) {
	path, err := Path(workDir)
	if err != nil {
		return nil, time.Time{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, time.Time{}, false
	}
	if len(f.Messages) == 0 {
		return nil, time.Time{}, false
	}
	return f.Messages, f.SavedAt, true
}

// Save writes messages as the current session for workDir, overwriting
// any previous save.
func Save(workDir string, messages []agent.Message) error {
	path, err := Path(workDir)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(file{SavedAt: time.Now(), Messages: messages})
	if err != nil {
		return err
	}

	// Write to a temp file and rename into place rather than writing path
	// directly: an in-place write truncates the existing file first, so a
	// crash (or any interruption) partway through leaves a truncated,
	// unparseable session behind. Rename is atomic on the same
	// filesystem — path always ends up as either the old content or the
	// complete new content, never a partial write.
	tmp, err := os.CreateTemp(dir, ".session-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Clear removes the saved session for workDir, if any. Not an error if
// there was nothing to remove.
func Clear(workDir string) error {
	path, err := Path(workDir)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
