package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unsetEnvForTest unsets key for the duration of the test, restoring
// whatever it was (set or not) afterward — t.Setenv alone can't express
// "make sure this is unset", only "set it to this value".
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	original, wasSet := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(key, original)
		}
	})
}

func TestUnquote(t *testing.T) {
	cases := map[string]string{
		`"sk-abc123"`: "sk-abc123",
		`'sk-abc123'`: "sk-abc123",
		"sk-abc123":   "sk-abc123",
		`"`:           `"`, // a single stray quote isn't a matching pair
		"":            "",
		`""`:          "",
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadDotEnvStripsQuotes is the regression test for a real,
// easy-to-hit bug: CHISEL_API_KEY="sk-..." in ~/.chisel.env is a
// natural way to write it (shell-style, quoting a value with special
// characters), but without unquoting, the literal quote characters
// become part of the key and every request fails authentication with
// no indication why.
func TestLoadDotEnvStripsQuotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnvForTest(t, "CHISEL_API_KEY")
	unsetEnvForTest(t, "CHISEL_MODEL")

	body := "CHISEL_API_KEY=\"sk-test-key\"\nCHISEL_MODEL='some-model'\n"
	if err := os.WriteFile(filepath.Join(home, ".chisel.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loadDotEnv()

	if got := os.Getenv("CHISEL_API_KEY"); got != "sk-test-key" {
		t.Errorf("CHISEL_API_KEY = %q, want quotes stripped", got)
	}
	if got := os.Getenv("CHISEL_MODEL"); got != "some-model" {
		t.Errorf("CHISEL_MODEL = %q, want quotes stripped", got)
	}
}

func TestLoadDotEnvRealEnvironmentWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHISEL_API_KEY", "real-value")

	body := "CHISEL_API_KEY=\"from-file\"\n"
	if err := os.WriteFile(filepath.Join(home, ".chisel.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loadDotEnv()

	if got := os.Getenv("CHISEL_API_KEY"); got != "real-value" {
		t.Errorf("CHISEL_API_KEY = %q, want the real environment variable to win over the file", got)
	}
}

func TestConfirmHooksTrustAcceptsYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !confirmHooksTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Error("expected trust to be granted on 'y'")
	}

	// A second call must not re-prompt — it's already trusted.
	if !confirmHooksTrustFrom(workDir, strings.NewReader("")) {
		t.Error("expected trust to persist without needing to answer again")
	}
}

func TestConfirmHooksTrustRejectsNoAndAnythingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if confirmHooksTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected trust to be denied on 'n'")
	}
	if confirmHooksTrustFrom(workDir, strings.NewReader("\n")) {
		t.Error("expected trust to be denied on a bare enter")
	}
}

func TestConfirmHooksTrustRePromptsOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !confirmHooksTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Fatal("expected initial trust to be granted")
	}

	// Change the hooks content — must require a fresh approval.
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"curl evil.example"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if confirmHooksTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected changed hooks content to require re-approval, not reuse the old trust")
	}
}
