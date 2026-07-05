// Package memory loads chisel's project-instructions file — the same
// convention as CLAUDE.md, GEMINI.md, and AGENTS.md — into the system
// prompt. Two layers: a user-level file for personal preferences that
// apply everywhere, and a project-level file for instructions specific
// to the repo chisel is running in.
package memory

import (
	"fmt"
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
				parts = append(parts, expandIncludes(filepath.Dir(path), text, 0))
				foundUser = true
			}
		}
	}

	if data, err := os.ReadFile(AgentsPath(workDir)); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, expandIncludes(workDir, text, 0))
			foundProject = true
		}
	}

	if data, err := os.ReadFile(ProjectPath(workDir)); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, expandIncludes(workDir, text, 0))
			foundProject = true
		}
	}

	return strings.Join(parts, "\n\n---\n\n"), foundUser, foundProject
}

// maxIncludeDepth bounds @include expansion's recursion — a cycle (A
// includes B includes A) or a self-include would otherwise recurse
// forever; a real, useful chain of includes is never this deep.
const maxIncludeDepth = 5

// expandIncludes replaces every line that's *entirely* "@path/to/file"
// (Gemini CLI's memory-import convention) with that file's own
// content, resolved relative to baseDir — the directory of the file
// currently being expanded, so a chain of includes each resolve
// relative to where they actually are, not the original file's
// location. Deliberately narrow about what counts as an include: only
// a line whose trimmed content is just "@" plus a path with no other
// text — "ask @someone about the deploy" mentioning something
// @-prefixed in prose is left untouched, the same reasoning
// internal/tui/fileref.go's @file references use for a token that
// doesn't resolve to a real file. A missing file, an unreadable one, or
// one that resolves outside baseDir (see resolveInclude) is silently
// left as the literal "@path" line rather than erroring the whole
// load — this is optional convenience, not config chisel depends on,
// matching Load's own treatment of a missing CHISEL.md.
func expandIncludes(baseDir, content string, depth int) string {
	if depth >= maxIncludeDepth {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		ref, ok := strings.CutPrefix(trimmed, "@")
		if !ok || ref == "" || strings.ContainsAny(ref, " \t") {
			continue
		}
		path, err := resolveInclude(baseDir, ref)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		included := strings.TrimSpace(string(data))
		lines[i] = expandIncludes(filepath.Dir(path), included, depth+1)
	}
	return strings.Join(lines, "\n")
}

// resolveInclude resolves ref (an @include's path) against baseDir,
// rejecting anything that would resolve outside it — the same
// escape-prevention every filesystem-touching tool in chisel applies
// (see agent.resolveInWorkDir's doc comment), scoped here to the
// including file's own directory rather than the whole project, since
// that's the boundary that actually makes sense for an include.
func resolveInclude(baseDir, ref string) (string, error) {
	full := filepath.Clean(filepath.Join(baseDir, ref))
	rel, err := filepath.Rel(baseDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("include path %q escapes its own directory", ref)
	}
	return full, nil
}
