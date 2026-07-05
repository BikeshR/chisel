// Package hooks runs user-configured shell commands around chisel's tool
// calls — a preToolUse hook can block a call outright (e.g. "refuse
// writes under vendor/"), a postToolUse hook runs after a successful call
// and its output is folded into the result the model sees (e.g. "run
// gofmt and tell the model if anything needs formatting").
//
// Config is project-scoped (.chisel/hooks.json in the working directory),
// not user-scoped like ~/.chisel/mcp.json — which hooks apply is a
// property of the project ("this repo runs gofmt after edits"), not the
// person running chisel, and the file is meant to be committed alongside
// the code it applies to.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// hookTimeout bounds a single hook command — hooks are meant to be quick
// checks or linters, not long-running processes.
var hookTimeout = 30 * time.Second

// Hook is one configured command. Match is a tool name (e.g.
// "str_replace_based_edit_tool") or "*" for every tool call — ignored
// for SessionStart, SessionEnd, and UserPromptSubmit, which have no
// tool call to match against; every hook configured under one of those
// three runs unconditionally.
type Hook struct {
	Match   string `json:"match"`
	Command string `json:"command"`
}

// Config is the top-level shape of .chisel/hooks.json.
type Config struct {
	Hooks struct {
		PreToolUse  []Hook `json:"preToolUse"`
		PostToolUse []Hook `json:"postToolUse"`
		// SessionStart fires once at startup, SessionEnd once at exit —
		// side-effect/notification hooks (a project setup check, a
		// logged session summary), not gates: neither can block
		// anything, since there's no call to refuse. UserPromptSubmit
		// fires before a submitted message reaches the model and *can*
		// block it, the same way a preToolUse hook blocks a tool call —
		// see RunUserPromptSubmit.
		SessionStart     []Hook `json:"sessionStart"`
		SessionEnd       []Hook `json:"sessionEnd"`
		UserPromptSubmit []Hook `json:"userPromptSubmit"`
	} `json:"hooks"`
}

// ConfigPath returns where chisel looks for hooks in workDir.
func ConfigPath(workDir string) string {
	return filepath.Join(workDir, ".chisel", "hooks.json")
}

// LoadConfig reads workDir's hook config. ok is false if the file simply
// doesn't exist (no hooks configured — not an error); a malformed file
// that does exist is still reported as an error.
func LoadConfig(workDir string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(ConfigPath(workDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

func matches(h Hook, toolName string) bool {
	return h.Match == "*" || h.Match == toolName
}

// RunPreToolUse runs every configured pre-hook matching toolName, in
// order, stopping at the first that blocks (a non-zero exit). blocked
// reports whether the call should be refused, and reason is what to tell
// the model — the blocking hook's own output if it printed anything.
func RunPreToolUse(ctx context.Context, workDir string, list []Hook, toolName, argsJSON, path string) (blocked bool, reason string, err error) {
	for _, h := range list {
		if !matches(h, toolName) {
			continue
		}
		output, exitCode, err := run(ctx, workDir, h, "CHISEL_HOOK_TOOL="+toolName, "CHISEL_HOOK_ARGS="+argsJSON, "CHISEL_HOOK_PATH="+path)
		if err != nil {
			return false, "", err
		}
		if exitCode != 0 {
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = fmt.Sprintf("blocked by hook %q (exit %d)", h.Command, exitCode)
			}
			return true, reason, nil
		}
	}
	return false, "", nil
}

// RunPostToolUse runs every configured post-hook matching toolName,
// collecting whatever they print to fold into the tool's result.
func RunPostToolUse(ctx context.Context, workDir string, list []Hook, toolName, argsJSON, path string) (string, error) {
	var out []string
	for _, h := range list {
		if !matches(h, toolName) {
			continue
		}
		output, _, err := run(ctx, workDir, h, "CHISEL_HOOK_TOOL="+toolName, "CHISEL_HOOK_ARGS="+argsJSON, "CHISEL_HOOK_PATH="+path)
		if err != nil {
			return "", err
		}
		if output = strings.TrimSpace(output); output != "" {
			out = append(out, output)
		}
	}
	return strings.Join(out, "\n"), nil
}

// RunSessionStart runs every configured SessionStart hook, in order,
// collecting whatever they print — informational only (there's no tool
// call, so nothing to block), meant to be shown to the user as a
// startup notice (a project setup check, for instance).
func RunSessionStart(ctx context.Context, workDir string, list []Hook) (string, error) {
	return runAllCollectingOutput(ctx, workDir, list)
}

// RunSessionEnd runs every configured SessionEnd hook, in order, for
// side effects only — logging a session summary, say. Its output isn't
// surfaced anywhere: chisel is already exiting by the time this runs,
// with nothing left to show it to.
func RunSessionEnd(ctx context.Context, workDir string, list []Hook) error {
	_, err := runAllCollectingOutput(ctx, workDir, list)
	return err
}

func runAllCollectingOutput(ctx context.Context, workDir string, list []Hook) (string, error) {
	var out []string
	for _, h := range list {
		output, _, err := run(ctx, workDir, h)
		if err != nil {
			return "", err
		}
		if output = strings.TrimSpace(output); output != "" {
			out = append(out, output)
		}
	}
	return strings.Join(out, "\n"), nil
}

// RunUserPromptSubmit runs every configured UserPromptSubmit hook, in
// order, stopping at the first that blocks (a non-zero exit) — the
// same contract RunPreToolUse has for a tool call, reacting instead to
// a message about to be sent to the model. Must be run through the
// same async tea.Cmd path preToolUse hooks already require (see
// internal/tui): a hook is an arbitrary shell command that can take
// real time, so it can't run synchronously on the Update goroutine.
func RunUserPromptSubmit(ctx context.Context, workDir string, list []Hook, text string) (blocked bool, reason string, err error) {
	for _, h := range list {
		output, exitCode, err := run(ctx, workDir, h, "CHISEL_HOOK_TEXT="+text)
		if err != nil {
			return false, "", err
		}
		if exitCode != 0 {
			reason := strings.TrimSpace(output)
			if reason == "" {
				reason = fmt.Sprintf("blocked by hook %q (exit %d)", h.Command, exitCode)
			}
			return true, reason, nil
		}
	}
	return false, "", nil
}

// run executes one hook command. extraEnv carries whatever the calling
// event needs available to the command (CHISEL_HOOK_TOOL/ARGS/PATH for
// a tool-call-shaped event, CHISEL_HOOK_TEXT for UserPromptSubmit, or
// nothing at all for SessionStart/SessionEnd, which react to no
// specific input).
func run(ctx context.Context, workDir string, h Hook, extraEnv ...string) (output string, exitCode int, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", h.Command)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), extraEnv...)
	// Without Setpgid + a Cancel override, context expiry only kills the
	// immediate sh process — a hook whose own foreground command runs
	// long after itself backgrounding a child (`sleep 300 &`, a daemon,
	// anything detached) left the whole group running well past the
	// timeout. Same pattern bash_background and BashSession already use
	// for exactly this reason: kill -pid, not just the tracked process.
	// WaitDelay covers the other shape — a hook that backgrounds a child
	// and returns immediately itself, so sh exits before the context
	// ever expires and there's no live process left for Cancel to act
	// on, but the orphaned child still holds the output pipe open;
	// WaitDelay bounds how long CombinedOutput waits on that pipe
	// regardless of whether Cancel ever fired.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	out, runErr := cmd.CombinedOutput()
	output = string(out)

	// A killed-on-timeout process still reports as some *exec.ExitError,
	// which would otherwise get misread as an ordinary (if unusual) exit
	// code rather than the distinct "this hook is hung" condition it is —
	// check the context first. timeoutCtx.Err() alone can't tell a real
	// hookTimeout expiry apart from the *parent* ctx (the turn itself)
	// being cancelled by esc — a user interrupting a turn one second
	// into a hook otherwise got told the hook "timed out after 30s",
	// misleading text the model would then see in the tool result too.
	// Same ctx-vs-derived-timeout distinction mcp.Server.call and
	// bashsession.go already make for exactly this reason.
	if timeoutCtx.Err() != nil {
		if ctx.Err() != nil {
			return output, -1, fmt.Errorf("hook %q interrupted", h.Command)
		}
		return output, -1, fmt.Errorf("hook %q timed out after %s", h.Command, hookTimeout)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return output, exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return output, -1, fmt.Errorf("run hook %q: %w", h.Command, runErr)
	}
	return output, 0, nil
}
