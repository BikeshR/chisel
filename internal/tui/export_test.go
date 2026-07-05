package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleExportCommandEmptyHistory(t *testing.T) {
	m := Model{}
	got := m.handleExportCommand(nil)
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing to export") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleExportCommandWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.md")
	m := Model{messages: []agent.Message{
		{Role: "user", Content: "fix the login bug"},
		{Role: "assistant", Content: "found it — the token wasn't refreshed"},
	}}
	got := m.handleExportCommand([]string{path})

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], path) {
		t.Errorf("lines = %+v, want a line confirming the export path", lines)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected the export file to exist: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "fix the login bug") || !strings.Contains(content, "found it") {
		t.Errorf("export content = %q, want both messages present", content)
	}
}

func TestHandleExportCommandDefaultPath(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	m := Model{messages: []agent.Message{{Role: "user", Content: "hi"}}}
	got := m.handleExportCommand(nil)

	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "chisel-export-") {
		t.Errorf("lines = %+v, want the default export filename mentioned", lines)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chisel-export-") && strings.HasSuffix(e.Name(), ".md") {
			found = true
		}
	}
	if !found {
		t.Errorf("directory entries = %+v, want a chisel-export-*.md file created", entries)
	}
}

func TestRenderTranscriptMarkdownIncludesToolCallsAndResults(t *testing.T) {
	messages := []agent.Message{
		{Role: "user", Content: "list the go files"},
		{Role: "assistant", ToolCalls: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: "main.go\nclient.go"},
		{Role: "assistant", Content: "found 2 files"},
	}
	got := renderTranscriptMarkdown(messages)

	for _, want := range []string{"list the go files", "glob", "**/*.go", "main.go", "found 2 files"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered markdown missing %q:\n%s", want, got)
		}
	}
}
