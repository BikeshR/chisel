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

func (s *BashSession) start() error {
	cmd := exec.Command("sh")
	cmd.Dir = s.workDir

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
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	s.cmd = nil
	s.stdin = nil
	s.reader = nil
}

// run sends one command followed by a sentinel line carrying the shell's
// exit code, then reads until that sentinel comes back. A non-zero exit
// code is reported in the output text (matching how a terminal shows it),
// not as a Go error — only a broken session (can't write, EOF, timeout)
// is an error here.
func (s *BashSession) run(ctx context.Context, command string) (string, error) {
	if _, err := fmt.Fprintf(s.stdin, "%s\necho \"%s$?\"\n", command, s.marker); err != nil {
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
		err    error
	}
	done := make(chan result, 1)

	go func() {
		var out strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if code, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), marker); ok {
				n, perr := strconv.Atoi(code)
				if perr != nil {
					done <- result{output: out.String(), err: fmt.Errorf("parse exit code from %q: %w", code, perr)}
					return
				}
				done <- result{output: out.String(), code: n}
				return
			}
			out.WriteString(line)
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
		return formatBashOutput(r.output, r.code), nil
	case <-timeoutCtx.Done():
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
