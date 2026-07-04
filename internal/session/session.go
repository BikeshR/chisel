// Package session persists chisel conversation history to disk, scoped by
// working directory, so closing and reopening chisel in the same project
// picks the conversation back up instead of starting cold every time.
package session

import (
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
// /home/brana/code/chisel becomes home-brana-code-chisel.
func sanitize(workDir string) string {
	s := strings.TrimPrefix(filepath.ToSlash(workDir), "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		s = "root"
	}
	return s
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(file{SavedAt: time.Now(), Messages: messages})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
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
