package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNeitherExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	content, foundUser, foundProject := Load(t.TempDir())
	if content != "" || foundUser || foundProject {
		t.Errorf("content=%q foundUser=%v foundProject=%v, want all empty/false", content, foundUser, foundProject)
	}
}

func TestLoadProjectOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(ProjectPath(workDir), []byte("use tabs not spaces"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, foundUser, foundProject := Load(workDir)
	if foundUser {
		t.Error("foundUser = true, want false")
	}
	if !foundProject {
		t.Error("foundProject = false, want true")
	}
	if !strings.Contains(content, "use tabs not spaces") {
		t.Errorf("content = %q", content)
	}
}

func TestLoadUserAndProjectAreLayered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	userPath, err := UserPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("always be terse"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(workDir), []byte("this repo uses gofmt"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, foundUser, foundProject := Load(workDir)
	if !foundUser || !foundProject {
		t.Fatalf("foundUser=%v foundProject=%v, want both true", foundUser, foundProject)
	}
	userIdx := strings.Index(content, "always be terse")
	projectIdx := strings.Index(content, "this repo uses gofmt")
	if userIdx == -1 || projectIdx == -1 {
		t.Fatalf("content = %q, want both files' content present", content)
	}
	if userIdx > projectIdx {
		t.Error("user-level content should come first (the base layer), project-level after (more specific)")
	}
}

func TestLoadIgnoresEmptyFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(ProjectPath(workDir), []byte("   \n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, foundProject := Load(workDir)
	if foundProject {
		t.Error("foundProject = true for a whitespace-only file")
	}
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
}
