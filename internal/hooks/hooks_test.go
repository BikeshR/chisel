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

// TestHookTimeoutKillsSlowForegroundGroupQuickly is the regression test
// for a real bug: on timeout, only the immediate sh process was killed,
// not its process group — a hook shaped like `sleep 10 & sleep 300`
// (a genuine repro: reliably took >30s to return against a 2s timeout
// before this fix) left the whole group running past the context
// deadline. With Setpgid + a Cancel override that kills -pid, both the
// slow foreground command and anything it backgrounded die together the
// moment the timeout fires, so this returns near-instantly rather than
// however long the foreground command itself would otherwise have run.
func TestHookTimeoutKillsSlowForegroundGroupQuickly(t *testing.T) {
	old := hookTimeout
	hookTimeout = 200 * time.Millisecond
	defer func() { hookTimeout = old }()

	dir := t.TempDir()
	// The foreground `sleep 10` keeps sh itself alive well past the
	// timeout — the scenario where Cancel actually gets a live process
	// to act on, unlike a hook that backgrounds a child and exits
	// immediately (see the WaitDelay-bounded test below).
	list := []Hook{{Match: "*", Command: "sleep 5 & sleep 10"}}

	start := time.Now()
	_, _, err := RunPreToolUse(context.Background(), dir, list, "bash", "{}", "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed > 1*time.Second {
		t.Errorf("RunPreToolUse took %s, want it bounded near hookTimeout (200ms) — killing the process group, not just sh, must stop the backgrounded child too", elapsed)
	}
}

// TestHookTimeoutBoundsOrphanedBackgroundChildByWaitDelay covers the
// other shape: a hook that backgrounds a child and returns immediately
// itself (sh exits before the context ever expires, so there's no live
// process left for Cancel to act on) but the orphaned child still holds
// the output pipe open. Without a nonzero WaitDelay this used to block
// CombinedOutput until that child exited on its own, however long that
// took — WaitDelay bounds it even when Cancel never fires.
func TestHookTimeoutBoundsOrphanedBackgroundChildByWaitDelay(t *testing.T) {
	old := hookTimeout
	hookTimeout = 200 * time.Millisecond
	defer func() { hookTimeout = old }()

	dir := t.TempDir()
	list := []Hook{{Match: "*", Command: "sleep 5 & echo done"}}

	start := time.Now()
	_, _, err := RunPreToolUse(context.Background(), dir, list, "bash", "{}", "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error (WaitDelay forcibly closing the pipes)")
	}
	if elapsed > 3*time.Second {
		t.Errorf("RunPreToolUse took %s, want it bounded near WaitDelay (2s), not the orphaned child's own 5s lifetime", elapsed)
	}
}
