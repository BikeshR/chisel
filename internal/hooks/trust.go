package hooks

import "github.com/BikeshR/chisel/internal/trust"

// Project-scoped hooks are, by design, arbitrary shell commands that run
// automatically — including on tool calls that need no permission at
// all (glob, grep). Cloning a hostile repo and asking chisel something
// as innocuous as "what does this project do?" would otherwise be
// enough to execute whatever .chisel/hooks.json says, with no
// confirmation whatsoever. This file is what makes that a one-time,
// explicit decision instead: trust is remembered per exact file
// content (not per path), so re-approval is only ever needed when the
// hooks themselves actually change. The actual content-hash-keyed
// store is internal/trust — shared with internal/permrules, which
// needs exactly the same one-time-approval mechanism for the same
// reason.
var trustStore = trust.Open("trusted_hooks.json")

// ContentHash returns a stable identifier for hooks.json's raw content.
func ContentHash(data []byte) string {
	return trust.ContentHash(data)
}

// IsTrusted reports whether hash has already been approved in a past run.
func IsTrusted(hash string) (bool, error) {
	return trustStore.IsTrusted(hash)
}

// Trust records hash as approved, persisting it for future runs.
func Trust(hash string) error {
	return trustStore.Trust(hash)
}

// HasAny reports whether cfg actually configures any hooks — an empty
// (but present) hooks.json shouldn't trigger a trust prompt for nothing.
func (c Config) HasAny() bool {
	return len(c.Hooks.PreToolUse) > 0 || len(c.Hooks.PostToolUse) > 0
}
