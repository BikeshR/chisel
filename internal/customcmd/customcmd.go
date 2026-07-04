// Package customcmd loads user-defined slash commands — Markdown files
// whose content becomes a canned prompt, expanded when invoked. Two
// layers, the same convention as internal/memory: a user-level
// directory for personal shortcuts that apply everywhere, and a
// project-level directory for commands specific to the repo chisel is
// running in — a project-level command overrides a user-level one of
// the same name.
//
// Unlike internal/hooks, a project-level command doesn't need a trust
// gate: it's canned prompt text, not code that executes automatically.
// Invoking one just sends that text through the exact same pipeline as
// anything the user types themselves — whatever the model does in
// response still goes through the normal permission gate. A hostile
// command template can suggest something harmful, the same way a
// hostile README could, but it can't do anything on its own.
package customcmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Command is one user-defined slash command.
type Command struct {
	Name     string // filename without the .md extension
	Template string // raw file content
}

// UserDir returns ~/.chisel/commands.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "commands"), nil
}

// ProjectDir returns <workDir>/.chisel/commands.
func ProjectDir(workDir string) string {
	return filepath.Join(workDir, ".chisel", "commands")
}

// Load reads every *.md file from the user-level and project-level
// commands directories, keyed by name (filename without extension). A
// project-level command overrides a user-level one with the same name.
// Neither directory existing, or a read error on an individual file, is
// silently skipped — this is optional convenience, not config chisel
// depends on, matching how ~/.chisel.env and memory files are treated.
func Load(workDir string) map[string]Command {
	commands := map[string]Command{}

	if dir, err := UserDir(); err == nil {
		loadDir(dir, commands)
	}
	loadDir(ProjectDir(workDir), commands)

	return commands
}

func loadDir(dir string, into map[string]Command) {
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
		into[name] = Command{Name: name, Template: string(data)}
	}
}

// Names returns every loaded command's name, sorted — for listing what's
// available.
func Names(commands map[string]Command) []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Expand substitutes every "$ARGUMENTS" in cmd.Template with args. If
// the template doesn't reference $ARGUMENTS at all but args was
// supplied anyway, args is appended at the end rather than silently
// discarded.
func Expand(cmd Command, args string) string {
	if strings.Contains(cmd.Template, "$ARGUMENTS") {
		return strings.ReplaceAll(cmd.Template, "$ARGUMENTS", args)
	}
	if args == "" {
		return cmd.Template
	}
	return strings.TrimRight(cmd.Template, "\n") + "\n\n" + args
}
