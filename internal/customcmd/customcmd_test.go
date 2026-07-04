package customcmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNoDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	commands := Load(t.TempDir())
	if len(commands) != 0 {
		t.Errorf("commands = %+v, want empty when no directories exist", commands)
	}
}

func TestLoadUserAndProjectCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "commands")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "review.md"), []byte("review this code for bugs"), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := ProjectDir(workDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "deploy.md"), []byte("run the deploy script"), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := Load(workDir)
	if len(commands) != 2 {
		t.Fatalf("got %d commands, want 2: %+v", len(commands), commands)
	}
	if commands["review"].Template != "review this code for bugs" {
		t.Errorf("review command = %+v", commands["review"])
	}
	if commands["deploy"].Template != "run the deploy script" {
		t.Errorf("deploy command = %+v", commands["deploy"])
	}
}

func TestLoadProjectOverridesUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "commands")
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

	commands := Load(workDir)
	if commands["test"].Template != "project version" {
		t.Errorf("test command = %q, want the project-level version to win", commands["test"].Template)
	}
}

func TestLoadIgnoresNonMarkdownFiles(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	dir := ProjectDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a command"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("a real command"), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := Load(workDir)
	if len(commands) != 1 {
		t.Fatalf("got %d commands, want 1 (non-.md files excluded): %+v", len(commands), commands)
	}
	if _, ok := commands["real"]; !ok {
		t.Error("expected the real.md command to be loaded")
	}
}

func TestNamesSorted(t *testing.T) {
	commands := map[string]Command{
		"zebra": {Name: "zebra"},
		"apple": {Name: "apple"},
		"mango": {Name: "mango"},
	}
	got := Names(commands)
	want := []string{"apple", "mango", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names() = %v, want %v", got, want)
		}
	}
}

func TestExpandSubstitutesArguments(t *testing.T) {
	cmd := Command{Template: "review the file $ARGUMENTS for bugs"}
	got := Expand(cmd, "main.go")
	want := "review the file main.go for bugs"
	if got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

func TestExpandAppendsArgumentsWhenTemplateHasNoPlaceholder(t *testing.T) {
	cmd := Command{Template: "review this code for bugs"}
	got := Expand(cmd, "focus on main.go")
	want := "review this code for bugs\n\nfocus on main.go"
	if got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

func TestExpandNoArgumentsLeavesTemplateUnchanged(t *testing.T) {
	cmd := Command{Template: "review this code for bugs"}
	got := Expand(cmd, "")
	if got != cmd.Template {
		t.Errorf("Expand() = %q, want unchanged template", got)
	}
}

func TestExpandMultiplePlaceholders(t *testing.T) {
	cmd := Command{Template: "compare $ARGUMENTS against $ARGUMENTS"}
	got := Expand(cmd, "v1")
	want := "compare v1 against v1"
	if got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}
