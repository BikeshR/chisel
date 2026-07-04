package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashSessionBasicCommand(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	out, err := s.Run(context.Background(), "echo hello", false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("output = %q, want %q", out, "hello")
	}
}

func TestBashSessionCdPersists(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewBashSession(workDir)
	defer s.Close()

	if _, err := s.Run(context.Background(), "cd subdir", false); err != nil {
		t.Fatalf("cd: %v", err)
	}

	out, err := s.Run(context.Background(), "pwd", false)
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	// Resolve symlinks on both sides — t.TempDir() can itself be a symlink
	// (e.g. /tmp -> /private/tmp on macOS), which would make a literal
	// string comparison fail even though the directory is genuinely right.
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("resolve pwd output %q: %v", out, err)
	}
	wantDir, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Errorf("pwd after cd = %q, want %q", gotDir, wantDir)
	}
}

func TestBashSessionEnvPersists(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	if _, err := s.Run(context.Background(), "export CHISEL_TEST_VAR=hello", false); err != nil {
		t.Fatalf("export: %v", err)
	}

	out, err := s.Run(context.Background(), "echo $CHISEL_TEST_VAR", false)
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("exported var did not persist: got %q", out)
	}
}

func TestBashSessionNonZeroExitIsNotAGoError(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	// A subshell exit — returns 7 to the persistent shell rather than
	// terminating it, the way a bare top-level `exit 7` would (that's
	// correct behavior, exercised separately: it should end the session).
	out, err := s.Run(context.Background(), "(exit 7)", false)
	if err != nil {
		t.Fatalf("a failing command should not be a Go error, got: %v", err)
	}
	if !strings.Contains(out, "exit status 7") {
		t.Errorf("output = %q, want it to mention exit status 7", out)
	}
}

func TestBashSessionTopLevelExitEndsSession(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	if _, err := s.Run(context.Background(), "exit 3", false); err == nil {
		t.Error("a top-level exit should end the session and surface as an error, matching a real terminal")
	}

	// The session must recover transparently on the next call.
	out, err := s.Run(context.Background(), "echo back", false)
	if err != nil {
		t.Fatalf("Run after session-ending exit: %v", err)
	}
	if strings.TrimSpace(out) != "back" {
		t.Errorf("output after recovery = %q", out)
	}
}

func TestBashSessionRestartClearsState(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewBashSession(workDir)
	defer s.Close()

	if _, err := s.Run(context.Background(), "cd subdir", false); err != nil {
		t.Fatalf("cd: %v", err)
	}

	msg, err := s.Run(context.Background(), "", true)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !strings.Contains(msg, "restart") {
		t.Errorf("restart message = %q, expected it to mention restarting", msg)
	}

	out, err := s.Run(context.Background(), "pwd", false)
	if err != nil {
		t.Fatalf("pwd after restart: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Errorf("pwd after restart = %q, want back to workDir %q", gotDir, wantDir)
	}
}

func TestBashSessionTimeout(t *testing.T) {
	old := bashCommandTimeout
	bashCommandTimeout = 200 * time.Millisecond
	defer func() { bashCommandTimeout = old }()

	s := NewBashSession(t.TempDir())
	defer s.Close()

	_, err := s.Run(context.Background(), "sleep 5", false)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want it to mention a timeout", err)
	}

	// The session must recover on the next call rather than staying wedged.
	out, err := s.Run(context.Background(), "echo recovered", false)
	if err != nil {
		t.Fatalf("Run after timeout: %v", err)
	}
	if strings.TrimSpace(out) != "recovered" {
		t.Errorf("output after recovery = %q", out)
	}
}

// TestBashSessionCallerCancellationIsNotReportedAsATimeout is the
// regression test for a subtlety the esc-to-interrupt feature depends
// on: a caller cancelling the passed-in ctx (not chisel's own
// bashCommandTimeout elapsing) must surface as context.Canceled, not the
// generic "command timed out" message — the TUI renders those two cases
// very differently ("interrupted" vs a real timeout warning).
func TestBashSessionCallerCancellationIsNotReportedAsATimeout(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := s.Run(ctx, "sleep 5", false)
	if err == nil {
		t.Fatal("expected an error after the caller cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want it distinguished from chisel's own bashCommandTimeout", err)
	}
}

func TestRunBashThroughSession(t *testing.T) {
	s := NewBashSession(t.TempDir())
	defer s.Close()

	input, err := json.Marshal(bashInput{Command: "echo via-runbash"})
	if err != nil {
		t.Fatal(err)
	}

	out, err := runBash(context.Background(), s, input)
	if err != nil {
		t.Fatalf("runBash: %v", err)
	}
	if strings.TrimSpace(out) != "via-runbash" {
		t.Errorf("output = %q", out)
	}
}

// TestRunBashNilSessionDoesNotPanic is the regression test for a
// nil-deref-if-unreachable risk: RunSubagent passes a nil *BashSession to
// Execute (subagentTools() never offers "bash", so it's never supposed to
// be dispatched), and nothing enforces that at the type level — a
// hallucinated "bash" tool call reaching here, however unlikely, must
// fail cleanly rather than panic on a nil receiver.
func TestRunBashNilSessionDoesNotPanic(t *testing.T) {
	input, err := json.Marshal(bashInput{Command: "echo hi"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = runBash(context.Background(), nil, input)
	if err == nil {
		t.Fatal("expected an error for a nil session, got nil")
	}
}
