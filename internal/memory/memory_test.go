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

// TestLoadReadsAgentsMdToo is the regression test for a real interop
// gap: opencode, Codex CLI, and Amp all read a project's AGENTS.md, but
// chisel — despite memory.go's own package doc already mentioning it —
// silently ignored one if it existed with no CHISEL.md alongside it.
func TestLoadReadsAgentsMdToo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(AgentsPath(workDir), []byte("shared instructions for every agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, foundUser, foundProject := Load(workDir)
	if foundUser {
		t.Error("foundUser = true, want false")
	}
	if !foundProject {
		t.Error("foundProject = false, want true — AGENTS.md alone should count")
	}
	if !strings.Contains(content, "shared instructions for every agent") {
		t.Errorf("content = %q, want AGENTS.md's content included", content)
	}
}

// TestLoadCombinesAgentsMdAndChiselMd confirms the two are additive
// (companion, not exclusive fallback) — a repo can have generic
// AGENTS.md content shared across tools plus chisel-specific additions
// in CHISEL.md, and both should reach the system prompt.
func TestLoadCombinesAgentsMdAndChiselMd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(AgentsPath(workDir), []byte("shared: use gofmt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(workDir), []byte("chisel-specific: be terse"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, foundProject := Load(workDir)
	if !foundProject {
		t.Fatal("foundProject = false, want true")
	}
	agentsIdx := strings.Index(content, "shared: use gofmt")
	chiselIdx := strings.Index(content, "chisel-specific: be terse")
	if agentsIdx == -1 || chiselIdx == -1 {
		t.Fatalf("content = %q, want both files' content present", content)
	}
	if agentsIdx > chiselIdx {
		t.Error("AGENTS.md (the shared, cross-tool layer) should come before CHISEL.md (chisel-specific additions)")
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
