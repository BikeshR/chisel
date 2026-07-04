package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGrepRejectsSymlinkEscape(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()

	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("super-secret-key-value"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A symlink inside workDir pointing at a file outside it — exactly
	// the "link -> ~/.ssh/id_ed25519" scenario.
	link := filepath.Join(workDir, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "super-secret"})

	out, err := runGrep(workDir, input)
	if err != nil {
		t.Fatalf("runGrep: %v", err)
	}
	if strings.Contains(out, "super-secret-key-value") {
		t.Fatalf("runGrep followed a symlink outside workDir and leaked its contents: %q", out)
	}
	if out != "(no matches)" {
		t.Errorf("output = %q, want no matches (the escaping symlink should be silently skipped)", out)
	}
}

func TestRunGrepStillFindsRealMatches(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "hello"})

	out, err := runGrep(workDir, input)
	if err != nil {
		t.Fatalf("runGrep: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "hello world") {
		t.Errorf("output = %q, want the real match found", out)
	}
}

// TestRunGrepSurvivesUnreadableDirectory is the regression test for a
// walk-abort bug: filepath.WalkDir's callback used to return the raw
// error for *any* problem reaching an entry, which aborts the entire
// walk — one permission-denied subdirectory (a root-owned docker volume
// mount, an odd .cache) made grep fail across the whole repo instead of
// just skipping that one directory.
func TestRunGrepSurvivesUnreadableDirectory(t *testing.T) {
	workDir := t.TempDir()

	blocked := filepath.Join(workDir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "inside.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(blocked, 0o755) }() // let t.TempDir's cleanup remove it

	if err := os.WriteFile(filepath.Join(workDir, "findme.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "hello"})

	out, err := runGrep(workDir, input)
	if err != nil {
		t.Fatalf("runGrep: %v (want it to skip the unreadable directory, not fail entirely)", err)
	}
	if !strings.Contains(out, "findme.txt") {
		t.Errorf("output = %q, want the match outside the unreadable directory to still be found", out)
	}
}

func TestRunGlobRejectsSymlinkEscape(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(workDir, "linkdir")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "**/*.txt"})

	out, err := runGlob(workDir, input)
	if err != nil {
		t.Fatalf("runGlob: %v", err)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("output = %q, want the escaping symlinked directory's contents excluded", out)
	}
}

func TestRunGlobExcludesSkipDirs(t *testing.T) {
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "node_modules", "dep.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "app.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "**/*.js"})

	out, err := runGlob(workDir, input)
	if err != nil {
		t.Fatalf("runGlob: %v", err)
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("output = %q, want node_modules excluded", out)
	}
	if !strings.Contains(out, "app.js") {
		t.Errorf("output = %q, want app.js (outside node_modules) included", out)
	}
}

// TestSkipDirsCoversCommonNonGoBuildDirectories is the regression test
// for a review finding: the original four-entry skipDirs list (.git,
// node_modules, vendor, .venv) covered Go/Node/Python well enough, but
// build output from most other ecosystems — a JS bundler's dist/build,
// Rust/Maven's target, Next.js's .next, Python's __pycache__ — would
// otherwise flood grep/glob results in those repos.
func TestSkipDirsCoversCommonNonGoBuildDirectories(t *testing.T) {
	for _, dir := range []string{"dist", "build", "target", ".next", "__pycache__"} {
		if !skipDirs[dir] {
			t.Errorf("skipDirs missing %q", dir)
		}
	}
}

func TestRunGrepExcludesSkipDirs(t *testing.T) {
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "dist", "bundle.js"), []byte("findme"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "source.js"), []byte("findme"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "findme"})

	out, err := runGrep(workDir, input)
	if err != nil {
		t.Fatalf("runGrep: %v", err)
	}
	if strings.Contains(out, "dist") {
		t.Errorf("output = %q, want dist/ excluded from the walk", out)
	}
	if !strings.Contains(out, "source.js") {
		t.Errorf("output = %q, want source.js (outside dist) found", out)
	}
}

func TestRunGlobCapsResults(t *testing.T) {
	workDir := t.TempDir()
	for i := 0; i < globResultLimit+25; i++ {
		name := fmt.Sprintf("file%03d.txt", i)
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	input, _ := json.Marshal(struct {
		Pattern string `json:"pattern"`
	}{Pattern: "*.txt"})

	out, err := runGlob(workDir, input)
	if err != nil {
		t.Fatalf("runGlob: %v", err)
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "25 more") {
		t.Errorf("output tail = %q, want a truncation marker mentioning 25 more matches", out[len(out)-80:])
	}
}
