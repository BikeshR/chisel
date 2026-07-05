package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSensitiveFile(t *testing.T) {
	cases := map[string]bool{
		".env":                  true,
		".env.local":            true,
		".env.production":       true,
		"id_rsa":                true,
		"id_ed25519":            true,
		"server.pem":            true,
		"credentials.json":      true,
		".npmrc":                true,
		".netrc":                true,
		"main.go":               false,
		"README.md":             false,
		"env.go":                false, // "env" without the leading dot/extension shape isn't a match
		".environment-setup.md": false,
	}
	for name, want := range cases {
		if got := isSensitiveFile(name); got != want {
			t.Errorf("isSensitiveFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIsSensitiveFileMatchesBaseNameRegardlessOfDirectory(t *testing.T) {
	if !isSensitiveFile("config/secrets/.env") {
		t.Error("expected a nested .env path to still match")
	}
}

func TestRunViewRefusesSensitiveFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("API_KEY=super-secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runView(dir, json.RawMessage(`{"path":".env"}`))
	if err == nil {
		t.Fatal("expected an error viewing a .env file")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Error("error message must not leak the file's content")
	}
}

func TestRunEditorViewCommandRefusesSensitiveFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"token":"abc123"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(editorInput{Command: "view", Path: "credentials.json"})
	_, err := runEditor(dir, input)
	if err == nil {
		t.Fatal("expected an error viewing credentials.json via the editor tool's view command")
	}
}

// TestRunEditorCreateStillAllowsSensitiveFile confirms the guard is
// read-scoped only — a user asking the model to help populate a fresh
// .env from .env.example is a legitimate, common task chisel shouldn't
// lose just because .env also matches the sensitive-file pattern.
func TestRunEditorCreateStillAllowsSensitiveFile(t *testing.T) {
	dir := t.TempDir()
	input, _ := json.Marshal(editorInput{Command: "create", Path: ".env", FileText: "DATABASE_URL=postgres://localhost/dev"})
	if _, err := runEditor(dir, input); err != nil {
		t.Fatalf("runEditor create on .env: %v, want it allowed — the guard is read-only", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "DATABASE_URL=postgres://localhost/dev" {
		t.Errorf("content = %q", data)
	}
}

func TestRunGrepSkipsSensitiveFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET_TOKEN=abc123xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("SECRET_TOKEN mentioned here too"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runGrep(dir, json.RawMessage(`{"pattern":"SECRET_TOKEN"}`))
	if err != nil {
		t.Fatalf("runGrep: %v", err)
	}
	if strings.Contains(out, "abc123xyz") {
		t.Errorf("output = %q, want .env's actual content never surfaced", out)
	}
	if !strings.Contains(out, "notes.txt") {
		t.Errorf("output = %q, want the match in the non-sensitive file still found", out)
	}
}

// TestExecuteConsultOracleCannotReadSensitiveFile is the end-to-end
// version through the real dispatch path a subagent/oracle actually
// takes.
func TestExecuteConsultOracleCannotReadSensitiveFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "id_rsa"), []byte("-----BEGIN PRIVATE KEY-----"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := ToolCall{ID: "call_1", Function: ToolCallFunction{Name: "view", Arguments: `{"path":"id_rsa"}`}}
	result := Execute(context.Background(), dir, "minimax-m3", call, nil, nil, nil, "")
	if !result.IsError {
		t.Fatal("expected an error result viewing id_rsa")
	}
	if strings.Contains(result.Content, "BEGIN PRIVATE KEY") {
		t.Error("result content must not leak the key material")
	}
}
