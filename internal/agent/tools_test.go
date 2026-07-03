package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func call(name, argsJSON string) ToolCall {
	return ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: argsJSON,
		},
	}
}

func TestNeedsPermission(t *testing.T) {
	cases := []struct {
		name string
		call ToolCall
		want bool
	}{
		{"bash always needs permission", call("bash", `{"command":"ls"}`), true},
		{"bash restart still needs permission", call("bash", `{"restart":true}`), true},
		{"editor view is read-only", call("str_replace_based_edit_tool", `{"command":"view","path":"a.go"}`), false},
		{"editor create needs permission", call("str_replace_based_edit_tool", `{"command":"create","path":"a.go"}`), true},
		{"editor str_replace needs permission", call("str_replace_based_edit_tool", `{"command":"str_replace","path":"a.go"}`), true},
		{"editor insert needs permission", call("str_replace_based_edit_tool", `{"command":"insert","path":"a.go"}`), true},
		{"glob is read-only", call("glob", `{"pattern":"**/*.go"}`), false},
		{"grep is read-only", call("grep", `{"pattern":"foo"}`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsPermission(c.call); got != c.want {
				t.Errorf("NeedsPermission(%s) = %v, want %v", c.call.Function.Name, got, c.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct {
		name string
		call ToolCall
		want string
	}{
		{"bash command", call("bash", `{"command":"go test ./..."}`), "run: go test ./..."},
		{"bash restart", call("bash", `{"restart":true}`), "bash (restart session)"},
		{"editor command", call("str_replace_based_edit_tool", `{"command":"create","path":"foo.go"}`), "create foo.go"},
		{"unknown tool falls back to name", call("mystery", `{}`), "mystery"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Summarize(c.call); got != c.want {
				t.Errorf("Summarize() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestToolResultToMessage(t *testing.T) {
	ok := ToolResult{ID: "call_1", Content: "42 files"}.ToMessage()
	if ok.Role != "tool" || ok.ToolCallID != "call_1" || ok.Content != "42 files" {
		t.Errorf("success case: got %+v", ok)
	}

	errResult := ToolResult{ID: "call_2", Content: "file not found", IsError: true}.ToMessage()
	if errResult.Content != "Error: file not found" {
		t.Errorf("error case: got content %q, want %q", errResult.Content, "Error: file not found")
	}
}

func TestResolveInWorkDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "existing.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative path to existing file", "existing.txt", false},
		{"relative path in subdirectory", "subdir/new.txt", false},
		{"not-yet-existing file to create", "new-file.txt", false},
		{"empty path rejected", "", true},
		{"simple traversal rejected", "../escape.txt", true},
		{"nested traversal rejected", "subdir/../../escape.txt", true},
		{"absolute path outside workDir rejected", filepath.Join(outsideDir, "escape.txt"), true},
		{"absolute path inside workDir allowed", filepath.Join(workDir, "existing.txt"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveInWorkDir(workDir, c.path)
			if (err != nil) != c.wantErr {
				t.Errorf("resolveInWorkDir(%q) error = %v, wantErr %v", c.path, err, c.wantErr)
			}
		})
	}
}

func TestResolveInWorkDirSymlinkEscape(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(workDir, "escape-link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	_, err := resolveInWorkDir(workDir, "escape-link/secret.txt")
	if err == nil {
		t.Error("expected a symlink escaping workDir to be rejected, got nil error")
	}
}
