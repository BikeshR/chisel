package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	want := ErrorContentPrefix + "file not found"
	if errResult.Content != want {
		t.Errorf("error case: got content %q, want %q", errResult.Content, want)
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
		{"nested path under a directory that doesn't exist yet", "newdir/newsub/new.go", false},
		{"deeply nested path under several missing directories", "a/b/c/d/new.go", false},
		{"nested traversal past a missing directory is still rejected", "newdir/../../escape.txt", true},
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

func TestTruncateOutputLeavesShortContentUnchanged(t *testing.T) {
	short := "hello world"
	if got := truncateOutput(short); got != short {
		t.Errorf("truncateOutput(%q) = %q, want unchanged", short, got)
	}
}

func TestTruncateOutputCutsAtRuneBoundary(t *testing.T) {
	// Each "é" is two UTF-8 bytes but one rune — a byte-based cut at
	// maxToolOutputChars could land mid-character and produce invalid
	// UTF-8; this confirms the cut is rune-based instead.
	long := strings.Repeat("é", maxToolOutputChars+100)
	got := truncateOutput(long)
	if !utf8.ValidString(got) {
		t.Fatal("truncateOutput produced invalid UTF-8")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("got = %q, want a truncation marker", got[len(got)-60:])
	}
}

func TestRunEditorCreateNestedDirectories(t *testing.T) {
	workDir := t.TempDir()
	input, _ := json.Marshal(editorInput{Command: "create", Path: "a/b/c/new.go", FileText: "package main\n"})

	out, err := runEditor(workDir, input)
	if err != nil {
		t.Fatalf("runEditor create: %v", err)
	}
	if !strings.Contains(out, "new.go") {
		t.Errorf("output = %q", out)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "a", "b", "c", "new.go"))
	if err != nil {
		t.Fatalf("expected the nested file to have been created: %v", err)
	}
	if string(data) != "package main\n" {
		t.Errorf("content = %q", data)
	}
}

// TestRunEditorCreateOverwriteDoesNotLeaveBackupFile is the regression
// test for removing createFile's .bak behavior — it predates the
// permission prompt's diff preview and /git auto, and with both in
// place a backup file chisel never mentions and never cleans up (and
// that /git auto would happily commit alongside everything else) had
// lost its reason to exist.
func TestRunEditorCreateOverwriteDoesNotLeaveBackupFile(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "existing.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(editorInput{Command: "create", Path: "existing.go", FileText: "package new\n"})
	if _, err := runEditor(workDir, input); err != nil {
		t.Fatalf("runEditor create: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package new\n" {
		t.Errorf("content = %q, want the file overwritten", data)
	}

	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a .bak file was created — that behavior should be gone")
	}
}

func TestReadFileInWorkDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("hello there"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFileInWorkDir(workDir, "notes.txt")
	if err != nil {
		t.Fatalf("ReadFileInWorkDir: %v", err)
	}
	if got != "hello there" {
		t.Errorf("got %q, want %q", got, "hello there")
	}
}

func TestReadFileInWorkDirRejectsEscape(t *testing.T) {
	workDir := t.TempDir()
	if _, err := ReadFileInWorkDir(workDir, "../../etc/passwd"); err == nil {
		t.Error("expected an error escaping the working directory")
	}
}
