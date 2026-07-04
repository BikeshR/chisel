package agent

import (
	"encoding/json"
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
