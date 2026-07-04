package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

func TestBashSessionCwdTracksActualDirectory(t *testing.T) {
	workDir := t.TempDir()
	sub := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewBashSession(workDir)
	defer s.Close()

	if got := s.Cwd(); got != "" {
		t.Errorf("Cwd() before any command = %q, want empty", got)
	}

	if _, err := s.Run(context.Background(), "echo hi", false); err != nil {
		t.Fatalf("echo: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(s.Cwd())
	if err != nil {
		t.Fatalf("resolve Cwd() %q: %v", s.Cwd(), err)
	}
	wantDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Errorf("Cwd() after a plain command = %q, want workDir %q", gotDir, wantDir)
	}

	if _, err := s.Run(context.Background(), "cd subdir", false); err != nil {
		t.Fatalf("cd: %v", err)
	}
	gotDir, err = filepath.EvalSymlinks(s.Cwd())
	if err != nil {
		t.Fatalf("resolve Cwd() %q: %v", s.Cwd(), err)
	}
	wantDir, err = filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Errorf("Cwd() after cd = %q, want %q", gotDir, wantDir)
	}
}

func TestBashSessionCwdResetsOnRestart(t *testing.T) {
	workDir := t.TempDir()
	s := NewBashSession(workDir)
	defer s.Close()

	if _, err := s.Run(context.Background(), "echo hi", false); err != nil {
		t.Fatal(err)
	}
	if s.Cwd() == "" {
		t.Fatal("expected Cwd() to be set after a command")
	}

	if _, err := s.Run(context.Background(), "", true); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := s.Cwd(); got != "" {
		t.Errorf("Cwd() after restart = %q, want empty", got)
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

// TestBashSessionTimeoutIncludesPartialOutput is the regression test
// for a real diagnostic-loss bug: a command that produced useful output
// before hanging used to have that output discarded entirely on
// timeout, leaving the model with nothing but "timed out" to go on.
func TestBashSessionTimeoutIncludesPartialOutput(t *testing.T) {
	old := bashCommandTimeout
	bashCommandTimeout = 300 * time.Millisecond
	defer func() { bashCommandTimeout = old }()

	s := NewBashSession(t.TempDir())
	defer s.Close()

	_, err := s.Run(context.Background(), "echo before-hang; sleep 5", false)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "before-hang") {
		t.Errorf("error = %v, want it to include the output produced before hanging", err)
	}
}

// TestBashSessionTimeoutKillsOrphanedChildren is the regression test for
// the process-group fix: before it, a timeout killed only the `sh`
// process itself, so anything it had backgrounded (a `npm run dev`, a
// forked build daemon) kept running invisibly after the session was
// torn down.
func TestBashSessionTimeoutKillsOrphanedChildren(t *testing.T) {
	old := bashCommandTimeout
	bashCommandTimeout = 300 * time.Millisecond
	defer func() { bashCommandTimeout = old }()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	s := NewBashSession(dir)
	defer s.Close()

	cmd := fmt.Sprintf("(sh -c 'echo $$ > %s; sleep 5') & disown; sleep 5", pidFile)
	if _, err := s.Run(context.Background(), cmd, false); err == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("child never wrote its pid: %v", readErr)
	}
	pid := strings.TrimSpace(string(pidBytes))

	// Signal 0 sends nothing — it's purely an existence/permission
	// check, so this doesn't disturb a legitimately-still-running
	// process (there shouldn't be one, but avoid assuming that).
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		_ = exec.Command("kill", "-9", pid).Run()
		t.Errorf("child process %s was still running after the session was torn down — the process-group kill did not reach it", pid)
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
