package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ToolCall is one function call the model made — the same shape whether
// it's freshly parsed off the wire or being round-tripped back into a
// later request's message history.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded object, as a string
}

// input returns the call's arguments as json.RawMessage, for handlers that
// unmarshal specific fields out of it.
func (c ToolCall) input() json.RawMessage {
	return json.RawMessage(c.Function.Arguments)
}

// ToolResult is what gets sent back to the model for a given ToolCall.ID.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
	// Usage is non-zero only for tools that make their own model
	// requests under the hood (dispatch_subagent) — everywhere else it's
	// the zero value, contributing nothing when a caller adds it into a
	// running total.
	Usage Usage
}

// ErrorContentPrefix marks a tool result's content as an error when
// folded into a "tool" role message — chat-completions has no dedicated
// wire field for this, so the model (and, when resuming a saved session,
// internal/tui/history.go) has to recover it from the text. Deliberately
// not a plain phrase like "Error: ": that's plausible as the literal
// start of genuine tool output too (a bash command's own stdout, a file
// whose first line happens to read that way), which would make a resumed
// session's history rendering mistake a real success for a failure. This
// is still clearly readable as "this failed", just a much
// lower-collision-risk marker than ordinary English.
const ErrorContentPrefix = "[chisel:error] "

// ToMessage renders a ToolResult as the "tool" message OpenAI's chat
// format expects. There's no dedicated wire field for "this was an
// error" — that convention folds it into the content text instead.
func (r ToolResult) ToMessage() Message {
	content := r.Content
	if r.IsError {
		content = ErrorContentPrefix + content
	}
	return Message{Role: "tool", ToolCallID: r.ID, Content: content}
}

// NeedsPermission reports whether a call must be confirmed by the user
// before it runs. Read-only tools (glob, grep, and editor "view") are
// auto-allowed; anything that touches the filesystem or runs a command
// is not.
func NeedsPermission(call ToolCall) bool {
	switch call.Function.Name {
	case "bash":
		return true
	case "str_replace_based_edit_tool":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(call.input(), &in)
		return in.Command != "view"
	default:
		return false
	}
}

// Summarize renders a one-line, human-readable description of a call for
// the permission prompt.
func Summarize(call ToolCall) string {
	switch call.Function.Name {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(call.input(), &in)
		if in.Command == "" {
			return "bash (restart session)"
		}
		return "run: " + in.Command
	case "str_replace_based_edit_tool":
		var in struct {
			Command string `json:"command"`
			Path    string `json:"path"`
		}
		_ = json.Unmarshal(call.input(), &in)
		return fmt.Sprintf("%s %s", in.Command, in.Path)
	case "dispatch_subagent":
		var in struct {
			Task string `json:"task"`
		}
		_ = json.Unmarshal(call.input(), &in)
		return "subagent: " + in.Task
	default:
		return call.Function.Name
	}
}

// Execute dispatches a call to its handler. workDir scopes every filesystem
// operation — chisel never touches anything outside it. bash runs against
// the given persistent session (nil is only valid if the call can't
// possibly be "bash" — every real caller should pass a live session).
// model is only used by dispatch_subagent, to run the child with the same
// model as the parent.
func Execute(ctx context.Context, workDir, model string, call ToolCall, bash *BashSession) ToolResult {
	var content string
	var usage Usage
	var err error

	switch call.Function.Name {
	case "bash":
		content, err = runBash(ctx, bash, call.input())
	case "str_replace_based_edit_tool":
		content, err = runEditor(workDir, call.input())
	case "glob":
		content, err = runGlob(workDir, call.input())
	case "grep":
		content, err = runGrep(workDir, call.input())
	case "view":
		content, err = runView(workDir, call.input())
	case "dispatch_subagent":
		content, usage, err = runDispatchSubagent(ctx, workDir, model, call.input())
	default:
		err = fmt.Errorf("unknown tool %q", call.Function.Name)
	}

	if err != nil {
		return ToolResult{ID: call.ID, Content: err.Error(), IsError: true, Usage: usage}
	}
	return ToolResult{ID: call.ID, Content: truncateOutput(content), Usage: usage}
}

// maxToolOutputChars caps how much text a single tool result can carry
// back to the model. Without this, one careless view of a large file, a
// `cat` of a big log, or an unfiltered glob could dump enough text into
// the conversation to seriously strain (or outright exceed) the API's
// request-size limits — and it isn't a one-time cost, since the full
// history including that result gets resent on every subsequent request
// until /compact or /new.
const maxToolOutputChars = 40_000

// truncateOutput cuts s to at most maxToolOutputChars runes, appending a
// marker noting how much was cut. Rune-based, not byte-based, so a
// multi-byte UTF-8 character is never split mid-sequence — see
// tui.truncateRunes for the same reasoning applied on the display side.
func truncateOutput(s string) string {
	runes := []rune(s)
	if len(runes) <= maxToolOutputChars {
		return s
	}
	return string(runes[:maxToolOutputChars]) + fmt.Sprintf("\n… truncated (%d more characters)", len(runes)-maxToolOutputChars)
}

// resolveInWorkDir resolves a model-supplied path against workDir and
// rejects anything that would escape it (absolute paths elsewhere, `..`
// traversal, or a symlink pointing outside). Non-existent paths (e.g. a
// file about to be created) are allowed as long as their resolved parent
// stays inside workDir.
func resolveInWorkDir(workDir, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}

	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	full = filepath.Clean(full)

	resolved, err := filepath.EvalSymlinks(full)
	switch {
	case err == nil:
		full = resolved
	case os.IsNotExist(err):
		// Path doesn't exist yet (create) — possibly several directory
		// levels deep, none of which exist yet either (a.go under a/b/
		// where b/ doesn't exist). Walk up to the nearest ancestor that
		// *does* exist and check that one stays inside root, rather than
		// requiring the immediate parent to already exist — creating
		// the missing intermediate directories themselves is the
		// caller's job (see createFile in editor.go), not this
		// function's; it only validates where the path would land.
		ancestor := filepath.Dir(full)
		for {
			resolvedAncestor, aerr := filepath.EvalSymlinks(ancestor)
			if aerr == nil {
				full = filepath.Join(resolvedAncestor, strings.TrimPrefix(full, ancestor))
				break
			}
			if !os.IsNotExist(aerr) {
				return "", aerr
			}
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				// Reached the filesystem root without finding anything
				// that exists — not reachable in practice (root always
				// exists), but avoids an infinite loop if it somehow were.
				return "", fmt.Errorf("no existing ancestor directory found for %s", full)
			}
			ancestor = parent
		}
	default:
		return "", err
	}

	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	return full, nil
}
