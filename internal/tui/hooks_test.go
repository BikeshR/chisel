package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/skill"
)

func TestToolPath(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "str_replace_based_edit_tool", Arguments: `{"command":"view","path":"foo.go"}`}}
	if got := toolPath(call); got != "foo.go" {
		t.Errorf("toolPath = %q, want %q", got, "foo.go")
	}

	noPath := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	if got := toolPath(noPath); got != "" {
		t.Errorf("toolPath = %q, want empty for a tool with no path arg", got)
	}
}

func TestExecuteToolBlockedByPreHook(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "protected.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreToolUse = []hooks.Hook{
		{Match: "str_replace_based_edit_tool", Command: `case "$CHISEL_HOOK_PATH" in protected.go) echo "protected file" >&2; exit 1;; esac`},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"protected.go","old_str":"package main","new_str":"package other"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	msg := cmd()

	result, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg, got %T", msg)
	}
	if !result.result.IsError {
		t.Fatal("expected the call to be blocked (IsError = true)")
	}
	if !strings.Contains(result.result.Content, "protected file") {
		t.Errorf("content = %q, want the hook's reason", result.result.Content)
	}

	// The file must be untouched — the block has to happen before execution.
	data, err := os.ReadFile(filepath.Join(dir, "protected.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "package other") {
		t.Error("file was modified despite being blocked by a preToolUse hook")
	}
}

func TestExecuteToolAllowedByPreHook(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreToolUse = []hooks.Hook{
		{Match: "str_replace_based_edit_tool", Command: "exit 0"},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"a.go","old_str":"package main","new_str":"package other"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	result := cmd().(toolResultMsg).result
	if result.IsError {
		t.Fatalf("call was blocked unexpectedly: %s", result.Content)
	}
}

func TestExecuteToolPostHookOutputAppended(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PostToolUse = []hooks.Hook{
		{Match: "str_replace_based_edit_tool", Command: "echo formatting looks fine"},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"a.go","old_str":"package main","new_str":"package other"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	result := cmd().(toolResultMsg).result
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "formatting looks fine") {
		t.Errorf("content = %q, want the post-hook's output appended", result.Content)
	}
}

func TestExecuteToolPostHookSkippedOnFailure(t *testing.T) {
	dir := t.TempDir()

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PostToolUse = []hooks.Hook{
		{Match: "str_replace_based_edit_tool", Command: "echo should-not-appear"},
	}

	// old_str won't be found — the edit itself fails.
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"nonexistent.go","old_str":"x","new_str":"y"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	result := cmd().(toolResultMsg).result
	if !result.IsError {
		t.Fatal("expected the edit itself to fail (file doesn't exist)")
	}
	if strings.Contains(result.Content, "should-not-appear") {
		t.Error("post-hook ran despite the tool call failing")
	}
}

// TestExecuteToolCapsCombinedPostHookOutput is the regression test for
// a real gap: every built-in tool's own output is capped via
// agent.TruncateOutput inside agent.Execute specifically because
// oversized content gets resent on every subsequent request until
// /compact, but a postToolUse hook's output was appended *after* that
// cap with no re-check — a verbose linter or formatter hook could push
// an otherwise-small result back over the same limit, uncapped.
func TestExecuteToolCapsCombinedPostHookOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PostToolUse = []hooks.Hook{
		// yes(1) with a byte count comfortably prints well past
		// maxToolOutputChars (40k) before head cuts it off.
		{Match: "str_replace_based_edit_tool", Command: "yes x | head -c 60000"},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"a.go","old_str":"package main","new_str":"package other"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	result := cmd().(toolResultMsg).result
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if len([]rune(result.Content)) > 41_000 {
		t.Errorf("result.Content is %d runes, want it capped near agent's maxToolOutputChars even after the post-hook's own output was appended", len([]rune(result.Content)))
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Error("expected a truncation marker in the combined output")
	}
}

func TestExecuteToolThreadsSkillsToLoadSkill(t *testing.T) {
	dir := t.TempDir()
	skills := map[string]skill.Skill{
		"go-review": {Name: "go-review", Content: "Check for unchecked errors."},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "load_skill",
		Arguments: `{"name":"go-review"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooks.Config{}, skills, nil, call)
	result := cmd().(toolResultMsg).result
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "Check for unchecked errors." {
		t.Errorf("content = %q, want the skill's content", result.Content)
	}
}

func TestExecuteToolBashBackgroundBlockedByPreHook(t *testing.T) {
	dir := t.TempDir()

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreToolUse = []hooks.Hook{
		{Match: "bash_background", Command: "echo blocked >&2; exit 1"},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "bash_background",
		Arguments: `{"command":"sleep 10"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	msg := cmd()

	result, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg for a hook-blocked call, got %T", msg)
	}
	if !result.result.IsError || !strings.Contains(result.result.Content, "blocked") {
		t.Errorf("result = %+v, want it blocked with the hook's reason", result.result)
	}
}

func TestExecuteToolBashBackgroundAllowedByPreHookReturnsBatch(t *testing.T) {
	dir := t.TempDir()

	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreToolUse = []hooks.Hook{
		{Match: "bash_background", Command: "exit 0"},
	}

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name:      "bash_background",
		Arguments: `{"command":"echo hi"}`,
	}}

	cmd := executeTool(context.Background(), dir, "minimax-m3", nil, nil, hooksCfg, nil, nil, call)
	msg := cmd()

	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg once the preToolUse hook allows it, got %T", msg)
	}
	if len(batch) != 3 {
		t.Errorf("got %d sub-commands, want 3", len(batch))
	}
}
