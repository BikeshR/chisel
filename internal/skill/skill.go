// Package skill loads user-defined skill files — Markdown files with a
// short frontmatter description and a body of full instructions, loaded
// on demand rather than always being part of the system prompt. Same
// two-layer convention as internal/memory and internal/customcmd: a
// user-level directory for skills that apply everywhere, and a
// project-level directory for skills specific to the repo chisel is
// running in — a project-level skill overrides a user-level one of the
// same name.
//
// Unlike a custom command (canned prompt text, sent as-is) a skill's
// description is what goes in the system prompt at startup — cheap,
// always present — while its full Content is only pulled into the
// conversation when the model actually calls load_skill for it (see
// internal/agent/skill.go), keeping an unused skill's cost near zero.
package skill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one user-defined skill.
type Skill struct {
	Name        string // filename without the .md extension
	Description string // from frontmatter — shown in the system prompt
	Content     string // the full body — only sent to the model via load_skill
}

// UserDir returns ~/.chisel/skills.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "skills"), nil
}

// ProjectDir returns <workDir>/.chisel/skills.
func ProjectDir(workDir string) string {
	return filepath.Join(workDir, ".chisel", "skills")
}

// Load reads every *.md file from the user-level and project-level
// skills directories, keyed by name (filename without extension). A
// project-level skill overrides a user-level one with the same name.
// Neither directory existing, or a read error on an individual file, is
// silently skipped — optional convenience, not config chisel depends on.
func Load(workDir string) map[string]Skill {
	skills := map[string]Skill{}

	if dir, err := UserDir(); err == nil {
		loadDir(dir, skills)
	}
	loadDir(ProjectDir(workDir), skills)

	return skills
}

func loadDir(dir string, into map[string]Skill) {
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

// parse splits a skill file into its frontmatter description and body.
// The frontmatter block is optional and deliberately minimal — just a
// "description:" line between "---" markers, not general YAML — no
// dependency is worth adding for one field.
func parse(name, text string) Skill {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Skill{Name: name, Content: strings.TrimSpace(text)}
	}
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return Skill{Name: name, Content: strings.TrimSpace(text)}
	}

	frontmatter, body := rest[:end], strings.TrimPrefix(rest[end+len("\n---"):], "\n")
	description := ""
	for _, line := range strings.Split(frontmatter, "\n") {
		if d, ok := strings.CutPrefix(line, "description:"); ok {
			description = strings.TrimSpace(d)
		}
	}
	return Skill{Name: name, Description: description, Content: strings.TrimSpace(body)}
}

// Names returns every loaded skill's name, sorted.
func Names(skills map[string]Skill) []string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
