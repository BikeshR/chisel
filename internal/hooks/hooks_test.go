package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigMissing(t *testing.T) {
	_, ok, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if ok {
		t.Error("ok = true with no hooks.json present")
	}
}

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	body := `{"hooks":{"preToolUse":[{"match":"bash","command":"exit 0"}],"postToolUse":[{"match":"*","command":"echo hi"}]}}`
	if err := os.MkdirAll(filepath.Join(dir, ".chisel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, ok, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false for a valid config")
	}
	if len(cfg.Hooks.PreToolUse) != 1 || cfg.Hooks.PreToolUse[0].Match != "bash" {
		t.Errorf("PreToolUse = %+v", cfg.Hooks.PreToolUse)
	}
	if len(cfg.Hooks.PostToolUse) != 1 || cfg.Hooks.PostToolUse[0].Match != "*" {
		t.Errorf("PostToolUse = %+v", cfg.Hooks.PostToolUse)
	}
}

func TestLoadConfigMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".chisel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(dir), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := LoadConfig(dir)
	if err == nil {
		t.Error("expected an error for malformed hooks.json, not a silent false")
	}
	if ok {
		t.Error("ok = true for a malformed config")
	}
}

func TestRunPreToolUseAllowed(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "bash", Command: "exit 0"}}

	blocked, reason, err := RunPreToolUse(context.Background(), dir, list, "bash", "{}", "")
	if err != nil {
		t.Fatalf("RunPreToolUse: %v", err)
	}
	if blocked {
		t.Errorf("blocked = true, want false; reason=%q", reason)
	}
}

func TestRunPreToolUseBlocked(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "str_replace_based_edit_tool", Command: `echo "no writes to vendor/" >&2; exit 1`}}

	blocked, reason, err := RunPreToolUse(context.Background(), dir, list, "str_replace_based_edit_tool", "{}", "vendor/foo.go")
	if err != nil {
		t.Fatalf("RunPreToolUse: %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if !strings.Contains(reason, "no writes to vendor/") {
		t.Errorf("reason = %q", reason)
	}
}

func TestRunPreToolUseDoesNotMatchOtherTools(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "bash", Command: "exit 1"}}

	blocked, _, err := RunPreToolUse(context.Background(), dir, list, "glob", "{}", "")
	if err != nil {
		t.Fatalf("RunPreToolUse: %v", err)
	}
	if blocked {
		t.Error("blocked = true for a tool the hook doesn't match")
	}
}

func TestRunPreToolUseWildcardMatchesEverything(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "*", Command: "exit 1"}}

	blocked, _, err := RunPreToolUse(context.Background(), dir, list, "glob", "{}", "")
	if err != nil {
		t.Fatalf("RunPreToolUse: %v", err)
	}
	if !blocked {
		t.Error("blocked = false, want true — \"*\" should match any tool")
	}
}

func TestRunPreToolUseEnvVars(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{
		Match:   "str_replace_based_edit_tool",
		Command: `[ "$CHISEL_HOOK_TOOL" = "str_replace_based_edit_tool" ] && [ "$CHISEL_HOOK_PATH" = "foo.go" ] && [ "$CHISEL_HOOK_ARGS" = '{"path":"foo.go"}' ]`,
	}}

	blocked, reason, err := RunPreToolUse(context.Background(), dir, list, "str_replace_based_edit_tool", `{"path":"foo.go"}`, "foo.go")
	if err != nil {
		t.Fatalf("RunPreToolUse: %v", err)
	}
	if blocked {
		t.Errorf("blocked = true (env vars didn't match as expected): %s", reason)
	}
}

func TestRunPostToolUseCollectsOutput(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{
		{Match: "str_replace_based_edit_tool", Command: "echo formatting-issue-in-foo"},
		{Match: "bash", Command: "echo should-not-run"}, // doesn't match
	}

	out, err := RunPostToolUse(context.Background(), dir, list, "str_replace_based_edit_tool", "{}", "foo.go")
	if err != nil {
		t.Fatalf("RunPostToolUse: %v", err)
	}
	if !strings.Contains(out, "formatting-issue-in-foo") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, "should-not-run") {
		t.Errorf("output = %q, want the non-matching hook's output excluded", out)
	}
}

func TestRunPostToolUseNoOutputWhenHooksAreQuiet(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "*", Command: "exit 0"}}

	out, err := RunPostToolUse(context.Background(), dir, list, "bash", "{}", "")
	if err != nil {
		t.Fatalf("RunPostToolUse: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty", out)
	}
}

func TestHookTimeout(t *testing.T) {
	old := hookTimeout
	hookTimeout = 200 * time.Millisecond
	defer func() { hookTimeout = old }()

	dir := t.TempDir()
	list := []Hook{{Match: "*", Command: "sleep 5"}}

	_, _, err := RunPreToolUse(context.Background(), dir, list, "bash", "{}", "")
	if err == nil {
		t.Fatal("expected an error for a hook that times out")
	}
}
