package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/hooks"
)

func TestHandleHooksCommandInitWritesScaffold(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	got := m.handleHooksCommand([]string{"init"})

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "wrote") {
		t.Errorf("lines = %+v, want a line confirming the scaffold was written", lines)
	}

	data, err := os.ReadFile(hooks.ConfigPath(m.workDir))
	if err != nil {
		t.Fatalf("expected .chisel/hooks.json to exist: %v", err)
	}

	cfg, ok, err := hooks.LoadConfig(m.workDir)
	if err != nil || !ok {
		t.Fatalf("scaffolded file doesn't parse as valid hooks.Config: ok=%v err=%v", ok, err)
	}
	if len(cfg.Hooks.PostToolUse) != 1 || cfg.Hooks.PostToolUse[0].Match != "str_replace_based_edit_tool" {
		t.Errorf("scaffolded config = %+v, want one postToolUse hook matching the editor tool", cfg.Hooks.PostToolUse)
	}
	if !strings.Contains(string(data), "gofmt") {
		t.Errorf("scaffold content = %q, want a real gofmt example, not just a placeholder", data)
	}
}

func TestHandleHooksCommandInitDoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path := hooks.ConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"preToolUse":[{"match":"bash","command":"exit 0"}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Model{workDir: dir}
	got := m.handleHooksCommand([]string{"init"})

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "already exists") {
		t.Errorf("lines = %+v, want a line refusing to overwrite the existing file", lines)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Errorf("hooks.json content = %q, want it unchanged", data)
	}
}

func TestHandleHooksCommandUsageForUnknownArg(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	got := m.handleHooksCommand([]string{"bogus"})
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v, want a usage line", lines)
	}
}

func TestHandleHooksCommandUsageForBareCommand(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	got := m.handleHooksCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "usage") {
		t.Errorf("lines = %+v, want a usage line for a bare /hooks", lines)
	}
}
