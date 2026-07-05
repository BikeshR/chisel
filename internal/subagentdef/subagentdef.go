// Package subagentdef loads user-defined custom subagents — Markdown
// files with a short frontmatter description, loaded at startup so the
// model can choose which one to delegate to via dispatch_subagent's
// optional "agent" parameter (see internal/agent's SetSubagents). Same
// two-layer convention and frontmatter format as internal/skill: a
// user-level directory for subagents that apply everywhere, a
// project-level directory for ones specific to the repo chisel is
// running in — a project-level definition overrides a user-level one
// of the same name.
//
// Deliberately no way for a definition to grant extra tools, bash, file
// edits, or dispatch_subagent itself — every custom subagent still runs
// with exactly the same fixed, read-only tool set (glob, grep, view)
// the built-in subagent always has (see agent.RunSubagent), which is
// what lets it skip the permission gate entirely: there's nothing in
// that tool set capable of mutating anything, by construction,
// regardless of what a definition's own prompt says. A definition only
// supplies a name, a description (so the model knows when to delegate
// to it), and a body of role-specific instructions layered on top of
// the task it's given — it can't widen what the role is capable of.
package subagentdef

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Subagent is one user-defined custom subagent role.
type Subagent struct {
	Name        string // filename without the .md extension
	Description string // from frontmatter — shown to the model so it can pick a role
	Prompt      string // the full body — layered on top of the task as role-specific instructions
}

// UserDir returns ~/.chisel/agents.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "agents"), nil
}

// ProjectDir returns <workDir>/.chisel/agents.
func ProjectDir(workDir string) string {
	return filepath.Join(workDir, ".chisel", "agents")
}

// Load reads every *.md file from the user-level and project-level
// agents directories, keyed by name (filename without extension). A
// project-level definition overrides a user-level one with the same
// name. Neither directory existing, or a read error on an individual
// file, is silently skipped — optional convenience, not config chisel
// depends on.
func Load(workDir string) map[string]Subagent {
	subagents := map[string]Subagent{}

	if dir, err := UserDir(); err == nil {
		loadDir(dir, subagents)
	}
	loadDir(ProjectDir(workDir), subagents)

	return subagents
}

func loadDir(dir string, into map[string]Subagent) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		into[name] = parse(name, string(data))
	}
}

// parse splits a subagent file into its frontmatter description and
// body — the exact same minimal "description:"-line-between-"---"
// convention internal/skill's parse uses, deliberately kept identical
// rather than introducing a second, slightly different frontmatter
// dialect for what's otherwise the same kind of file.
func parse(name, text string) Subagent {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Subagent{Name: name, Prompt: strings.TrimSpace(text)}
	}
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return Subagent{Name: name, Prompt: strings.TrimSpace(text)}
	}

	frontmatter, body := rest[:end], strings.TrimPrefix(rest[end+len("\n---"):], "\n")
	description := ""
	for _, line := range strings.Split(frontmatter, "\n") {
		if d, ok := strings.CutPrefix(line, "description:"); ok {
			description = strings.TrimSpace(d)
		}
	}
	return Subagent{Name: name, Description: description, Prompt: strings.TrimSpace(body)}
}

// Names returns every loaded subagent's name, sorted.
func Names(subagents map[string]Subagent) []string {
	names := make([]string, 0, len(subagents))
	for name := range subagents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
