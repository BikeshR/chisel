// Package trust implements one small, generic mechanism — remembering
// that a specific file's exact content has been approved to do
// something that would otherwise run without confirmation — shared by
// every project-scoped file chisel loads that can affect what runs
// automatically. internal/hooks (arbitrary shell commands that run on
// tool calls) was the first; internal/permrules (rules that can
// silently pre-approve a tool call that would otherwise need
// confirmation) is the second. Both need exactly the same
// content-hash-keyed, one-time-approval store, so it lives here once.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// ContentHash returns a stable identifier for a file's raw content.
// Keyed by content rather than path so the same file checked out in a
// different clone of the same repo doesn't need re-approval, but any
// actual change to it — even subtle — does.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Store remembers which content hashes have already been approved,
// persisted under ~/.chisel/<filename>. Separate callers (hooks,
// permission rules) each get their own filename, so trusting one kind
// of file never implicitly trusts the other.
type Store struct {
	filename string
}

// Open returns a Store backed by ~/.chisel/<filename>. filename should
// be specific to what's being trusted (e.g. "trusted_hooks.json") —
// this doesn't create anything on disk until Trust is first called.
func Open(filename string) Store {
	return Store{filename: filename}
}

func (s Store) path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", s.filename), nil
}

type trustFile struct {
	Trusted map[string]bool `json:"trusted"`
}

func (s Store) load() (trustFile, error) {
	path, err := s.path()
	if err != nil {
		return trustFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return trustFile{Trusted: map[string]bool{}}, nil
		}
		return trustFile{}, err
	}
	var tf trustFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return trustFile{}, err
	}
	if tf.Trusted == nil {
		tf.Trusted = map[string]bool{}
	}
	return tf, nil
}

// IsTrusted reports whether hash has already been approved in a past run.
func (s Store) IsTrusted(hash string) (bool, error) {
	tf, err := s.load()
	if err != nil {
		return false, err
	}
	return tf.Trusted[hash], nil
}

// Trust records hash as approved, persisting it for future runs.
func (s Store) Trust(hash string) error {
	tf, err := s.load()
	if err != nil {
		return err
	}
	tf.Trusted[hash] = true

	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
