package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNoDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skills := Load(t.TempDir())
	if len(skills) != 0 {
		t.Errorf("skills = %+v, want empty when no directories exist", skills)
	}
}

func TestParseWithFrontmatter(t *testing.T) {
	text := "---\ndescription: reviews Go code for common bugs\n---\nFull instructions here.\nMore detail.\n"
	got := parse("go-review", text)

	if got.Name != "go-review" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Description != "reviews Go code for common bugs" {
		t.Errorf("Description = %q", got.Description)
	}
	want := "Full instructions here.\nMore detail."
	if got.Content != want {
		t.Errorf("Content = %q, want %q", got.Content, want)
	}
}

func TestParseWithoutFrontmatter(t *testing.T) {
	got := parse("plain", "just the content, no frontmatter at all")
	if got.Description != "" {
		t.Errorf("Description = %q, want empty", got.Description)
	}
	if got.Content != "just the content, no frontmatter at all" {
		t.Errorf("Content = %q", got.Content)
	}
}

func TestParseMalformedFrontmatterFallsBackToWholeFile(t *testing.T) {
	text := "---\ndescription: unterminated frontmatter\nno closing marker"
	got := parse("broken", text)
	if got.Description != "" {
		t.Errorf("Description = %q, want empty when frontmatter never closes", got.Description)
	}
	if got.Content != text {
		t.Errorf("Content = %q, want the whole file treated as content", got.Content)
	}
}

func TestLoadUserAndProjectSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "skills")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "writing.md"), []byte("---\ndescription: house style for prose\n---\nBe concise."), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDir := ProjectDir(workDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "deploy.md"), []byte("---\ndescription: how to deploy this repo\n---\nRun make deploy."), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := Load(workDir)
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(skills), skills)
	}
	if skills["writing"].Description != "house style for prose" {
		t.Errorf("writing skill = %+v", skills["writing"])
	}
	if skills["deploy"].Content != "Run make deploy." {
		t.Errorf("deploy skill = %+v", skills["deploy"])
	}
}

func TestLoadProjectOverridesUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userDir := filepath.Join(home, ".chisel", "skills")
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

	skills := Load(workDir)
	if skills["test"].Content != "project version" {
		t.Errorf("test skill = %q, want the project-level version to win", skills["test"].Content)
	}
}

func TestNamesSorted(t *testing.T) {
	skills := map[string]Skill{"zebra": {}, "apple": {}, "mango": {}}
	got := Names(skills)
	want := []string{"apple", "mango", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names() = %v, want %v", got, want)
		}
	}
}
