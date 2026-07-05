// Package agentmemory implements chisel's own agent-writable, persistent
// per-project memory — distinct from internal/memory's user-authored
// CHISEL.md/AGENTS.md, which the *user* writes for the model to read.
// This file is one the *model* writes to itself, via the "remember"
// tool, whenever it learns something worth persisting past the current
// session (a convention, a gotcha, a stated preference) — loaded back
// into the system prompt on every future run in this project, the same
// way CHISEL.md is, but under its own clearly labeled section so a user
// (and the model) can tell "instructions I was given" apart from "notes
// I made for myself."
//
// Deliberately project-scoped only (<workDir>/.chisel/MEMORY.md, not a
// second user-level tier) — most of what's worth remembering this way
// ("this repo uses tabs," "run gofmt before committing") is specific to
// the project it was learned in; anything genuinely universal already
// has a place, the user's own ~/.chisel/CHISEL.md.
package agentmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const filename = "MEMORY.md"

// maxBytes caps MEMORY.md's total size — mirrors the discipline other
// agent-writable-memory implementations use (Claude Code caps its own
// around 25KB) so it costs a bounded, predictable amount of every
// future request's system prompt rather than growing without limit
// across many sessions. Remember enforces this by dropping the oldest
// entries first, not by refusing new ones.
const maxBytes = 25_000

// Path returns <workDir>/.chisel/MEMORY.md.
func Path(workDir string) string {
	return filepath.Join(workDir, ".chisel", filename)
}

// Load reads workDir's agent memory file, if present — a missing file
// isn't an error, just "nothing remembered yet in this project".
func Load(workDir string) (content string, found bool) {
	data, err := os.ReadFile(Path(workDir))
	if err != nil {
		return "", false
	}
	text := strings.TrimSpace(string(data))
	return text, text != ""
}

// Remember appends note as a new entry, trimming the oldest entries
// first if the result would exceed maxBytes. Newlines in note are
// flattened to spaces — each entry is stored as one line so reloading
// can split entries back apart unambiguously; a "note" long/detailed
// enough to need real structure belongs in CHISEL.md, written by a
// human, not this tool.
func Remember(workDir, note string) error {
	note = strings.Join(strings.Fields(note), " ")
	if note == "" {
		return fmt.Errorf("note is empty")
	}

	entries := loadEntries(workDir)
	entries = append(entries, note)
	for joined(entries) > maxBytes && len(entries) > 1 {
		entries = entries[1:]
	}

	return writeEntries(workDir, entries)
}

// Clear removes workDir's agent memory file entirely — used by
// /memory clear. Not an error if there was nothing to clear.
func Clear(workDir string) error {
	err := os.Remove(Path(workDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadEntries(workDir string) []string {
	content, found := Load(workDir)
	if !found {
		return nil
	}
	var entries []string
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, "- "); ok {
			entries = append(entries, rest)
		}
	}
	return entries
}

func joined(entries []string) int {
	n := 0
	for _, e := range entries {
		n += len(e) + 3 // "- " prefix plus the trailing newline
	}
	return n
}

func writeEntries(workDir string, entries []string) error {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	return writeAtomic(Path(workDir), []byte(b.String()))
}

// writeAtomic writes data to path via a temp file plus rename — the
// same pattern internal/session, internal/trust, and internal/permrules
// already use, for the same reason: a plain os.WriteFile can leave a
// truncated, corrupt MEMORY.md behind if chisel is killed mid-write.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".memory-*.md.tmp")
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
