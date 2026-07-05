// Package memory loads chisel's project-instructions file — the same
// convention as CLAUDE.md, GEMINI.md, and AGENTS.md — into the system
// prompt. Two layers: a user-level file for personal preferences that
// apply everywhere, and a project-level file for instructions specific
// to the repo chisel is running in.
package memory

import (
	"os"
	"path/filepath"
	"strings"
)

const filename = "CHISEL.md"

// UserPath returns ~/.chisel/CHISEL.md.
func UserPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", filename), nil
}

// ProjectPath returns <workDir>/CHISEL.md.
func ProjectPath(workDir string) string {
	return filepath.Join(workDir, filename)
}

// AgentsPath returns <workDir>/AGENTS.md — read as an additional
// project-level source alongside CHISEL.md, not instead of it: opencode,
// Codex CLI, and Amp have all independently converged on this filename
// as a de facto cross-tool standard for project instructions, so a repo
// that already has one written for those tools shouldn't be silently
// ignored just because it isn't named CHISEL.md.
func AgentsPath(workDir string) string {
	return filepath.Join(workDir, "AGENTS.md")
}

// Load reads the user-level and project-level memory files, if present —
// none existing, or a read error on any of them, is fine and not
// reported as an error, matching how ~/.chisel.env is treated: this is
// optional context, not config chisel depends on. Layered
// oldest/most-generic first: the user-level CHISEL.md (personal
// preference, applies everywhere), then the project's AGENTS.md if one
// exists (the shared, cross-tool layer), then the project's own
// CHISEL.md (chisel-specific additions on top of whatever AGENTS.md
// already says). foundProject is true if either project-level file was
// found — chisel doesn't track which one specifically past that point,
// since both feed the same system-prompt section the same way.
func Load(workDir string) (content string, foundUser, foundProject bool) {
	var parts []string

	if path, err := UserPath(); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if text := strings.TrimSpace(string(data)); text != "" {
				parts = append(parts, text)
				foundUser = true
			}
		}
	}

	if data, err := os.ReadFile(AgentsPath(workDir)); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, text)
			foundProject = true
		}
	}

	if data, err := os.ReadFile(ProjectPath(workDir)); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, text)
			foundProject = true
		}
	}

	return strings.Join(parts, "\n\n---\n\n"), foundUser, foundProject
}
