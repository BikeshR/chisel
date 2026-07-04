package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// bashCommandTimeout bounds a single command. On timeout the underlying
// shell process is killed and replaced — its output is desynced from this
// point, so there's no safe way to keep using it. A var, not a const, so
// tests can shorten it rather than waiting out the real value.
var bashCommandTimeout = 2 * time.Minute

// BashSession is a persistent shell chisel's bash tool runs commands
// through, so `cd` and exported environment variables carry from one call
// to the next — the same way a real terminal session works, and unlike a
// fresh `sh -c` subprocess per call. It's started lazily on first use and
// lives for as long as the process that owns it (see tui.New), independent
// of which model is currently selected — switching models must not lose
// shell state.
//
// Known limitation shared with every bash tool that scripts a shell over a
// single stdin/stdout pipe: a command that itself reads from stdin (an
// interactive prompt, `read` with no input redirected) will consume the
// sentinel line meant to mark its own completion, desyncing the session.
// There's no general fix short of not doing this over a pipe at all.
type BashSession struct {
	workDir string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	marker string
	// cwd is the shell's current directory, refreshed after every
	// command — so a caller (the permission prompt, in particular) can
	// tell whether `cd` has taken the persistent shell somewhere other
	// than workDir without needing its own round-trip through the shell.
	cwd string
}

// NewBashSession creates a session rooted at workDir. The underlying shell
// isn't started until the first command runs.
func NewBashSession(workDir string) *BashSession {
	return &BashSession{workDir: workDir}
}

// Run executes command in the persistent shell. restart kills and
// discards the current shell (if any) instead of running a command,
// dropping all cd/env state back to a fresh shell rooted at workDir.
func (s *BashSession) Run(ctx context.Context, command string, restart bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if restart {
		s.stop()
		return "shell session restarted", nil
	}

	if s.cmd == nil {
		if err := s.start(); err != nil {
			return "", fmt.Errorf("start shell: %w", err)
		}
	}

	output, err := s.run(ctx, command)
	if err != nil {
		// The session is in an unknown state after any infrastructure
		// error (timeout, crash, EOF) — drop it so the next call starts
		// clean instead of reading output left over from this one.
		s.stop()
	}
	return output, err
}

// Close shuts down the underlying shell, if one is running. Safe to call
// even if no command has ever run.
func (s *BashSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
}

// Cwd returns the shell's current directory as of the last command run,
// or "" if no command has ever run (the shell hasn't started, or was
// just restarted). Safe to call concurrently with Run — it takes the
// same lock.
func (s *BashSession) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *BashSession) start() error {
	cmd := exec.Command("sh")
	cmd.Dir = s.workDir
	// Its own process group, so stop() can kill every descendant a
	// command spawned (a backgrounded `npm run dev`, a forked build
	// daemon) — without this, killing only the `sh` process itself on
	// timeout left orphaned children running indefinitely, invisibly
	// holding whatever port or lock they'd acquired.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	marker, err := randomMarker()
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}

	s.cmd = cmd
	s.stdin = stdin
	s.reader = bufio.NewReader(stdout)
	s.marker = marker

	// Merge stderr into the one stream being read, for the life of the
	// session — a plain `sh` with StdoutPipe has nowhere else to send it.
	if _, err := s.run(context.Background(), "exec 2>&1"); err != nil {
		s.stop()
		return fmt.Errorf("initialize shell: %w", err)
	}
	return nil
}

func (s *BashSession) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.stdin.Close()
	// Negative PID targets the whole process group (see start's
	// Setpgid) rather than just the shell itself — a plain
	// cmd.Process.Kill() would leave anything the shell had spawned
	// (and not waited on) still running after the session is gone.
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	_ = s.cmd.Wait()
	s.cmd = nil
	s.stdin = nil
	s.reader = nil
	s.cwd = ""
}

// run sends one command followed by a sentinel line carrying the shell's
// exit code, then reads until that sentinel comes back. A non-zero exit
// code is reported in the output text (matching how a terminal shows it),
// not as a Go error — only a broken session (can't write, EOF, timeout)
// is an error here.
func (s *BashSession) run(ctx context.Context, command string) (string, error) {
	// $PWD rides along on the same sentinel line as the exit code — one
	// extra round-trip-free way to keep s.cwd current after every
	// command, not just ones that look like `cd`. Splitting on the
	// first ':' is unambiguous even if $PWD itself contains a colon
	// (rare, but not impossible): the exit code is always all-digits, so
	// it can never contain the delimiter itself.
	if _, err := fmt.Fprintf(s.stdin, "%s\necho \"%s$?:$PWD\"\n", command, s.marker); err != nil {
		return "", fmt.Errorf("write command: %w", err)
	}

	// Captured locally rather than read as s.reader/s.marker inside the
	// goroutine below: on timeout, run returns while that goroutine is
	// still alive, and a later call's stop()/start() can concurrently nil
	// out or replace those fields — an unsynchronized read of s.reader
	// there would race (and could nil-deref, or start reading the next
	// session's output entirely) with no lock protecting the goroutine
	// itself.
	reader := s.reader
	marker := s.marker

	type result struct {
		output string
		code   int
		cwd    string
		err    error
	}
	done := make(chan result, 1)

	// partial mirrors the goroutine's own output builder so a timeout
	// (below) can report whatever the command had printed up to that
	// point, instead of discarding it — a command that hangs after
	// producing useful diagnostics used to leave the model with nothing
	// to go on but "timed out". Guarded by its own mutex since the
	// goroutine keeps running (and keeps writing to it) after run
	// returns on timeout; this isn't reused for the done-channel path,
	// which has no such race.
	var partialMu sync.Mutex
	var partial strings.Builder

	go func() {
		var out strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), marker); ok {
				codeStr, cwd, _ := strings.Cut(rest, ":")
				n, perr := strconv.Atoi(codeStr)
				if perr != nil {
					done <- result{output: out.String(), err: fmt.Errorf("parse exit code from %q: %w", codeStr, perr)}
					return
				}
				done <- result{output: out.String(), code: n, cwd: cwd}
				return
			}
			out.WriteString(line)
			partialMu.Lock()
			partial.WriteString(line)
			partialMu.Unlock()
			if readErr != nil {
				done <- result{output: out.String(), err: fmt.Errorf("shell session ended: %w", readErr)}
				return
			}
		}
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, bashCommandTimeout)
	defer cancel()

	select {
	case r := <-done:
		if r.err != nil {
			return r.output, r.err
		}
		if r.cwd != "" {
			s.cwd = r.cwd
		}
		return formatBashOutput(r.output, r.code), nil
	case <-timeoutCtx.Done():
		// timeoutCtx.Done can fire for two different reasons: bashCommandTimeout
		// actually elapsed, or the caller cancelled ctx itself (esc, while
		// busy) — check ctx.Err(), not timeoutCtx.Err(), to tell them apart,
		// since a caller-cancelled ctx should read as "interrupted", not
		// "timed out", even though both derive the same Done() signal.
		if ctx.Err() != nil {
			// Returned bare (not wrapped with any partial output) so it
			// stays exactly context.Canceled — the TUI's interrupted-vs-
			// error display matches on that value directly.
			return "", ctx.Err()
		}
		partialMu.Lock()
		partialOutput := partial.String()
		partialMu.Unlock()
		if partialOutput != "" {
			return "", fmt.Errorf("command timed out after %s; output so far:\n%s", bashCommandTimeout, partialOutput)
		}
		return "", fmt.Errorf("command timed out after %s", bashCommandTimeout)
	}
}

func formatBashOutput(output string, exitCode int) string {
	if exitCode != 0 {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += fmt.Sprintf("exit status %d", exitCode)
	}
	if output == "" {
		output = "(no output)"
	}
	return output
}

func randomMarker() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "__CHISEL_DONE_" + hex.EncodeToString(b) + "__", nil
}
