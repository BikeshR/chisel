// Package history persists chisel's prompt input-recall history (see
// internal/tui's up/down recall and ctrl+r reverse search) globally
// across every working directory chisel is run in — one shared history,
// the same scope a shell's own command history has, unlike
// internal/session's conversations, which are scoped per project.
package history

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// maxEntries caps how many entries are kept — without this, a long-lived
// install accumulates one entry per submitted message forever.
const maxEntries = 1000

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "history.json"), nil
}

// Load returns the persisted history, oldest first — empty (not an
// error) if there's nothing saved yet, or the file can't be read or
// parsed. A corrupt history file isn't worth failing startup over or
// even warning about (unlike a corrupt session — see session.Load's own
// corrupt distinction): losing recall history is a much smaller loss
// than losing a conversation, so this just starts fresh silently.
func Load() []string {
	p, err := path()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var entries []string
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	return entries
}

// Append adds text to the persisted history, trimming to maxEntries
// (dropping the oldest) if needed, and writes the result back out
// immediately — so history survives even if chisel exits abnormally,
// not just a clean shutdown.
func Append(text string) error {
	entries := append(Load(), text)
	if len(entries) > maxEntries {
		entries = entries[len(entries)-maxEntries:]
	}
	return save(entries)
}

// save writes entries atomically — a temp file plus rename, the same
// pattern session.Save uses and for the same reason: an in-place write
// truncates the existing file first, so a crash mid-write would
// otherwise leave a corrupt, unparseable history behind rather than
// just losing whatever this one Append was trying to add.
func save(entries []string) error {
	p, err := path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".history-*.json.tmp")
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
	return os.Rename(tmpPath, p)
}
