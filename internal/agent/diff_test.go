package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editCall(command, argsJSON string) ToolCall {
	return ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: ToolCallFunction{
			Name:      "str_replace_based_edit_tool",
			Arguments: `{"command":"` + command + `",` + argsJSON,
		},
	}
}

func TestPreviewEditStrReplace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := editCall("str_replace", `"path":"a.go","old_str":"func old() {}","new_str":"func new() {}"}`)
	diff, ok := PreviewEdit(dir, call)
	if !ok {
		t.Fatal("PreviewEdit: ok = false, want true")
	}
	if !strings.Contains(diff, "-func old() {}") || !strings.Contains(diff, "+func new() {}") {
		t.Errorf("diff = %q, want it to show the replacement", diff)
	}

	// The file itself must be untouched — this is a preview only.
	data, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "func old() {}") {
		t.Error("PreviewEdit wrote to disk, it should only compute a diff")
	}
}

func TestPreviewEditInsert(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := editCall("insert", `"path":"a.go","insert_line":1,"insert_text":"inserted"}`)
	diff, ok := PreviewEdit(dir, call)
	if !ok {
		t.Fatal("PreviewEdit: ok = false, want true")
	}
	if !strings.Contains(diff, "+inserted") {
		t.Errorf("diff = %q, want it to show the inserted line", diff)
	}
}

func TestPreviewEditCreateNewFile(t *testing.T) {
	dir := t.TempDir()

	call := editCall("create", `"path":"new.go","file_text":"package main\n"}`)
	diff, ok := PreviewEdit(dir, call)
	if !ok {
		t.Fatal("PreviewEdit: ok = false, want true")
	}
	if !strings.Contains(diff, "+package main") {
		t.Errorf("diff = %q, want it to show the new content as all-added", diff)
	}
}

func TestPreviewEditCreateOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("old content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := editCall("create", `"path":"a.go","file_text":"new content\n"}`)
	diff, ok := PreviewEdit(dir, call)
	if !ok {
		t.Fatal("PreviewEdit: ok = false, want true")
	}
	if !strings.Contains(diff, "-old content") || !strings.Contains(diff, "+new content") {
		t.Errorf("diff = %q, want it to show old vs new", diff)
	}
}

func TestPreviewEditNotApplicable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("view command", func(t *testing.T) {
		_, ok := PreviewEdit(dir, editCall("view", `"path":"a.go"}`))
		if ok {
			t.Error("expected no diff for a view command")
		}
	})

	t.Run("other tool", func(t *testing.T) {
		call := ToolCall{Function: ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
		_, ok := PreviewEdit(dir, call)
		if ok {
			t.Error("expected no diff for a non-editor tool")
		}
	})

	t.Run("old_str not found", func(t *testing.T) {
		call := editCall("str_replace", `"path":"a.go","old_str":"nonexistent","new_str":"x"}`)
		_, ok := PreviewEdit(dir, call)
		if ok {
			t.Error("expected no diff when old_str can't be found — the real error surfaces at execution instead")
		}
	})
}
