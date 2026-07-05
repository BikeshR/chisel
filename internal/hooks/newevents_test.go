package hooks

import (
	"context"
	"strings"
	"testing"
)

func TestRunSessionStartCollectsOutput(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Command: "echo starting up"}}

	out, err := RunSessionStart(context.Background(), dir, list)
	if err != nil {
		t.Fatalf("RunSessionStart: %v", err)
	}
	if !strings.Contains(out, "starting up") {
		t.Errorf("out = %q, want the hook's output", out)
	}
}

func TestRunSessionStartNoOutputWhenNoneConfigured(t *testing.T) {
	out, err := RunSessionStart(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("RunSessionStart: %v", err)
	}
	if out != "" {
		t.Errorf("out = %q, want empty with no hooks configured", out)
	}
}

// TestRunSessionStartIgnoresMatch confirms Match is irrelevant here —
// every configured hook runs regardless of what Match is set to, since
// there's no tool call to filter by.
func TestRunSessionStartIgnoresMatch(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Match: "bash", Command: "echo ran anyway"}}

	out, err := RunSessionStart(context.Background(), dir, list)
	if err != nil {
		t.Fatalf("RunSessionStart: %v", err)
	}
	if !strings.Contains(out, "ran anyway") {
		t.Errorf("out = %q, want the hook to run despite an unrelated Match value", out)
	}
}

func TestRunSessionEndRunsCommandAndReportsErrors(t *testing.T) {
	dir := t.TempDir()

	if err := RunSessionEnd(context.Background(), dir, []Hook{{Command: "exit 0"}}); err != nil {
		t.Errorf("RunSessionEnd: %v, want nil for a successful hook", err)
	}
}

func TestRunUserPromptSubmitAllowed(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Command: "exit 0"}}

	blocked, _, err := RunUserPromptSubmit(context.Background(), dir, list, "hello")
	if err != nil {
		t.Fatalf("RunUserPromptSubmit: %v", err)
	}
	if blocked {
		t.Error("blocked = true, want false")
	}
}

func TestRunUserPromptSubmitBlocked(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Command: `echo "message contains a secret" >&2; exit 1`}}

	blocked, reason, err := RunUserPromptSubmit(context.Background(), dir, list, "here is my api key: xyz")
	if err != nil {
		t.Fatalf("RunUserPromptSubmit: %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if !strings.Contains(reason, "message contains a secret") {
		t.Errorf("reason = %q", reason)
	}
}

func TestRunUserPromptSubmitTextEnvVar(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{{Command: `[ "$CHISEL_HOOK_TEXT" = "hello world" ]`}}

	blocked, reason, err := RunUserPromptSubmit(context.Background(), dir, list, "hello world")
	if err != nil {
		t.Fatalf("RunUserPromptSubmit: %v", err)
	}
	if blocked {
		t.Errorf("blocked = true (CHISEL_HOOK_TEXT didn't match as expected): %s", reason)
	}
}

func TestRunUserPromptSubmitStopsAtFirstBlock(t *testing.T) {
	dir := t.TempDir()
	list := []Hook{
		{Command: `echo "first hook blocked it" >&2; exit 1`},
		{Command: `echo "second hook should never run" >&2; exit 1`},
	}

	blocked, reason, err := RunUserPromptSubmit(context.Background(), dir, list, "text")
	if err != nil {
		t.Fatalf("RunUserPromptSubmit: %v", err)
	}
	if !blocked {
		t.Fatal("blocked = false, want true")
	}
	if strings.Contains(reason, "second hook") {
		t.Errorf("reason = %q, want only the first blocking hook's output", reason)
	}
}
