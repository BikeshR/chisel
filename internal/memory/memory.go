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

// Load reads the user-level and project-level memory files, if present —
// neither existing, or a read error on either, is fine and not reported
// as an error, matching how ~/.chisel.env is treated: this is optional
// context, not config chisel depends on. The user-level file comes first
// (a base layer of personal preference), the project-level file after
// (more specific to what's being worked on right now).
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

	if data, err := os.ReadFile(ProjectPath(workDir)); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, text)
			foundProject = true
		}
	}

	return strings.Join(parts, "\n\n---\n\n"), foundUser, foundProject
}
