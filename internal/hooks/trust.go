package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Project-scoped hooks are, by design, arbitrary shell commands that run
// automatically — including on tool calls that need no permission at
// all (glob, grep). Cloning a hostile repo and asking chisel something
// as innocuous as "what does this project do?" would otherwise be
// enough to execute whatever .chisel/hooks.json says, with no
// confirmation whatsoever. This file is what makes that a one-time,
// explicit decision instead: trust is remembered per exact file
// content (not per path), so re-approval is only ever needed when the
// hooks themselves actually change.

// TrustStorePath returns where chisel remembers which hooks.json
// contents have already been approved to run.
func TrustStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "trusted_hooks.json"), nil
}

// ContentHash returns a stable identifier for hooks.json's raw content.
// Keyed by content rather than path so the same hooks.json checked out
// in a different clone of the same repo doesn't need re-approval, but
// any actual change to the hooks — even subtle — does.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type trustStore struct {
	Trusted map[string]bool `json:"trusted"`
}

func loadTrustStore() (trustStore, error) {
	path, err := TrustStorePath()
	if err != nil {
		return trustStore{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return trustStore{Trusted: map[string]bool{}}, nil
		}
		return trustStore{}, err
	}
	var ts trustStore
	if err := json.Unmarshal(data, &ts); err != nil {
		return trustStore{}, err
	}
	if ts.Trusted == nil {
		ts.Trusted = map[string]bool{}
	}
	return ts, nil
}

// IsTrusted reports whether hash has already been approved in a past run.
func IsTrusted(hash string) (bool, error) {
	ts, err := loadTrustStore()
	if err != nil {
		return false, err
	}
	return ts.Trusted[hash], nil
}

// Trust records hash as approved, persisting it for future runs.
func Trust(hash string) error {
	ts, err := loadTrustStore()
	if err != nil {
		return err
	}
	ts.Trusted[hash] = true

	path, err := TrustStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// HasAny reports whether cfg actually configures any hooks — an empty
// (but present) hooks.json shouldn't trigger a trust prompt for nothing.
func (c Config) HasAny() bool {
	return len(c.Hooks.PreToolUse) > 0 || len(c.Hooks.PostToolUse) > 0
}
