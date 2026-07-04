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
	"time"
)

// hookTimeout bounds a single hook command — hooks are meant to be quick
// checks or linters, not long-running processes.
var hookTimeout = 30 * time.Second

// Hook is one configured command. Match is a tool name (e.g.
// "str_replace_based_edit_tool") or "*" for every tool call.
type Hook struct {
	Match   string `json:"match"`
	Command string `json:"command"`
}

// Config is the top-level shape of .chisel/hooks.json.
type Config struct {
	Hooks struct {
		PreToolUse  []Hook `json:"preToolUse"`
		PostToolUse []Hook `json:"postToolUse"`
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
		output, exitCode, err := run(ctx, workDir, h, toolName, argsJSON, path)
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
		output, _, err := run(ctx, workDir, h, toolName, argsJSON, path)
		if err != nil {
			return "", err
		}
		if output = strings.TrimSpace(output); output != "" {
			out = append(out, output)
		}
	}
	return strings.Join(out, "\n"), nil
}

// run executes one hook command, with the call it's reacting to available
// both as environment variables (CHISEL_HOOK_TOOL/ARGS/PATH — the simple
// case, e.g. checking $CHISEL_HOOK_PATH against a pattern needs no JSON
// parsing at all) and, for anything more elaborate, CHISEL_HOOK_ARGS
// carries the tool call's raw JSON arguments.
func run(ctx context.Context, workDir string, h Hook, toolName, argsJSON, path string) (output string, exitCode int, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", h.Command)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"CHISEL_HOOK_TOOL="+toolName,
		"CHISEL_HOOK_ARGS="+argsJSON,
		"CHISEL_HOOK_PATH="+path,
	)

	out, runErr := cmd.CombinedOutput()
	output = string(out)

	// A killed-on-timeout process still reports as some *exec.ExitError,
	// which would otherwise get misread as an ordinary (if unusual) exit
	// code rather than the distinct "this hook is hung" condition it is —
	// check the context first.
	if timeoutCtx.Err() != nil {
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
