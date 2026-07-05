package subagentdef

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNoDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	subagents := Load(t.TempDir())
	if len(subagents) != 0 {
		t.Errorf("subagents = %+v, want empty when no directories exist", subagents)
	}
}

func TestParseWithFrontmatter(t *testing.T) {
	text := "---\ndescription: reviews Go code for common bugs\n---\nYou are a Go code review specialist.\nFocus on correctness.\n"
	got := parse("go-reviewer", text)

	if got.Name != "go-reviewer" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Description != "reviews Go code for common bugs" {
		t.Errorf("Description = %q", got.Description)
	}
	want := "You are a Go code review specialist.\nFocus on correctness."
	if got.Prompt != want {
		t.Errorf("Prompt = %q, want %q", got.Prompt, want)
	}
}

func TestParseWithoutFrontmatter(t *testing.T) {
	got := parse("plain", "just the prompt, no frontmatter at all")
	if got.Description != "" {
		t.Errorf("Description = %q, want empty", got.Description)
	}
	if got.Prompt != "just the prompt, no frontmatter at all" {
		t.Errorf("Prompt = %q", got.Prompt)
	}
}

func TestParseMalformedFrontmatterFallsBackToWholeFile(t *testing.T) {
	text := "---\ndescription: unterminated frontmatter\nno closing marker"
	got := parse("broken", text)
	if got.Description != "" {
		t.Errorf("Description = %q, want empty when frontmatter never closes", got.Description)
	}
	if got.Prompt != text {
		t.Errorf("Prompt = %q, want the whole file treated as the prompt", got.Prompt)
	}
}

func TestLoadUserAndProjectSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "agents")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "researcher.md"), []byte("---\ndescription: general research role\n---\nBe thorough."), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := ProjectDir(workDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "security-reviewer.md"), []byte("---\ndescription: audits for security issues\n---\nLook for injection and auth bugs."), 0o644); err != nil {
		t.Fatal(err)
	}

	subagents := Load(workDir)
	if len(subagents) != 2 {
		t.Fatalf("got %d subagents, want 2: %+v", len(subagents), subagents)
	}
	if subagents["researcher"].Description != "general research role" {
		t.Errorf("researcher subagent = %+v", subagents["researcher"])
	}
	if subagents["security-reviewer"].Prompt != "Look for injection and auth bugs." {
		t.Errorf("security-reviewer subagent = %+v", subagents["security-reviewer"])
	}
}

func TestLoadProjectOverridesUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "agents")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "test.md"), []byte("user version"), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := ProjectDir(workDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "test.md"), []byte("project version"), 0o644); err != nil {
		t.Fatal(err)
	}

	subagents := Load(workDir)
	if subagents["test"].Prompt != "project version" {
		t.Errorf("test subagent = %q, want the project-level version to win", subagents["test"].Prompt)
	}
}

func TestNamesSorted(t *testing.T) {
	subagents := map[string]Subagent{"zebra": {}, "apple": {}, "mango": {}}
	got := Names(subagents)
	want := []string{"apple", "mango", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names() = %v, want %v", got, want)
		}
	}
}
