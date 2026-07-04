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
}

// ToMessage renders a ToolResult as the "tool" message OpenAI's chat
// format expects. There's no dedicated wire field for "this was an
// error" — that convention folds it into the content text instead.
func (r ToolResult) ToMessage() Message {
	content := r.Content
	if r.IsError {
		content = "Error: " + content
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
		content, err = runDispatchSubagent(ctx, workDir, model, call.input())
	default:
		err = fmt.Errorf("unknown tool %q", call.Function.Name)
	}

	if err != nil {
		return ToolResult{ID: call.ID, Content: err.Error(), IsError: true}
	}
	return ToolResult{ID: call.ID, Content: content}
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
		// Path doesn't exist yet (create). Check its parent instead.
		parent, perr := filepath.EvalSymlinks(filepath.Dir(full))
		if perr != nil {
			return "", fmt.Errorf("parent directory does not exist: %s", filepath.Dir(full))
		}
		full = filepath.Join(parent, filepath.Base(full))
	default:
		return "", err
	}

	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the working directory", p)
	}
	return full, nil
}
