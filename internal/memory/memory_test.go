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

// TestLoadExpandsIncludeInProjectFile is the direct regression test for
// the feature: a bare "@shared.md" line in CHISEL.md should be replaced
// with shared.md's own content, resolved relative to the project directory.
func TestLoadExpandsIncludeInProjectFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shared.md"), []byte("shared conventions here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(workDir), []byte("project notes\n@shared.md\nmore notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, foundProject := Load(workDir)
	if !foundProject {
		t.Fatal("foundProject = false, want true")
	}
	if !strings.Contains(content, "shared conventions here") {
		t.Errorf("content = %q, want the included file's content present", content)
	}
	if strings.Contains(content, "@shared.md") {
		t.Errorf("content = %q, want the literal @shared.md line replaced, not left as-is", content)
	}
}

// TestLoadDoesNotTreatInlineAtMentionAsInclude confirms only a line
// that's *entirely* "@path" is treated as an include — prose that
// merely mentions something starting with "@" must survive unchanged.
func TestLoadDoesNotTreatInlineAtMentionAsInclude(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(ProjectPath(workDir), []byte("ask @someone about the deploy process"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, _ := Load(workDir)
	if !strings.Contains(content, "ask @someone about the deploy process") {
		t.Errorf("content = %q, want the prose left untouched", content)
	}
}

// TestLoadIncludeMissingFileLeftAsLiteralLine confirms a reference to a
// file that doesn't exist doesn't error the whole load — it's optional
// convenience, same as a missing CHISEL.md itself.
func TestLoadIncludeMissingFileLeftAsLiteralLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(ProjectPath(workDir), []byte("@does-not-exist.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, foundProject := Load(workDir)
	if !foundProject {
		t.Fatal("foundProject = false, want true — the file itself exists even if its include target doesn't")
	}
	if !strings.Contains(content, "@does-not-exist.md") {
		t.Errorf("content = %q, want the unresolvable include left as a literal line", content)
	}
}

// TestLoadIncludeRejectsPathEscapingItsOwnDirectory confirms an
// @include can't traverse outside the including file's own directory —
// the same escape-prevention every filesystem-touching path in chisel
// applies.
func TestLoadIncludeRejectsPathEscapingItsOwnDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := t.TempDir()

	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.md"), []byte("top secret content"), 0o644); err != nil {
		t.Fatal(err)
	}

	rel, err := filepath.Rel(workDir, filepath.Join(outsideDir, "secret.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(workDir), []byte("@"+rel), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, _ := Load(workDir)
	if strings.Contains(content, "top secret content") {
		t.Errorf("content = %q, want the escaping include rejected, not followed", content)
	}
}

// TestLoadIncludeExpandsRecursively confirms a chain of includes (A
// includes B, B includes C) all resolve, each relative to its own file's
// directory.
func TestLoadIncludeExpandsRecursively(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "b.md"), []byte("content of b\n@c.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "c.md"), []byte("content of c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(workDir), []byte("@b.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, _, _ := Load(workDir)
	if !strings.Contains(content, "content of b") || !strings.Contains(content, "content of c") {
		t.Errorf("content = %q, want both levels of the include chain expanded", content)
	}
}

// TestExpandIncludesStopsAtMaxDepth is the safety-critical test for a
// self-referencing cycle — without a depth bound, "a.md" including
// itself would recurse forever. Calling this directly (not through
// Load, which would itself never return on a real cycle) is itself
// proof it terminates — a hung test process is exactly what "no depth
// bound" would look like here.
func TestExpandIncludesStopsAtMaxDepth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("@a.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := expandIncludes(dir, "@a.md", 0)
	if got == "" {
		t.Error("expandIncludes returned empty, want the unexpanded line left in place once the depth bound is hit")
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
