package tui

import (
	"bytes"
	"encoding/json"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/mcp"
)

// permissionDecision is the outcome of checking whether a tool call may
// run without asking. Centralizing this (decidePermission, below) was a
// direct response to a review finding that four different code paths —
// agent.NeedsPermission, the MCP always-ask rule, plan mode's hard-deny,
// and hooks' preToolUse block — each independently decided "don't run
// this" with differently worded, differently rendered refusals. Hooks
// are deliberately NOT part of this: a preToolUse hook is an arbitrary
// shell command that can take real time, so it has to run through the
// same async Cmd as the tool call itself (see executeTool in model.go),
// not a synchronous check made before the permission prompt even
// appears — that asymmetry is fine; it's the decision and the
// presentation that needed one path, not every check needing to be
// synchronous.
type permissionDecision int

const (
	permissionAllow permissionDecision = iota
	permissionAsk
	permissionDeny
)

// decidePermission is the single place that decides allow/ask/deny for
// a tool call about to be dispatched. allowlist is checked only for
// calls that would otherwise need confirmation, and only after the plan
// mode check — plan mode is meant to be an absolute guarantee that
// nothing runs, so a call the user previously always-allowed must still
// be blocked while plan mode is on, not silently exempted from it.
func decidePermission(call agent.ToolCall, planMode bool, allowlist map[string]bool) (decision permissionDecision, reason string) {
	needsConfirmation := needsPermission(call)

	if needsConfirmation && planMode {
		return permissionDeny, "Not run — chisel is in plan mode, which only allows read-only exploration. Describe this as part of your plan instead, then stop; the user will exit plan mode before you make any changes."
	}
	if needsConfirmation {
		if key, ok := allowlistKey(call); ok && allowlist[key] {
			return permissionAllow, ""
		}
		return permissionAsk, ""
	}
	return permissionAllow, ""
}

// needsPermission reports whether call must be confirmed before running.
// Every MCP-sourced tool always needs permission — chisel has no way to
// know what an arbitrary server's tool actually does, so it can't apply
// the same read-only auto-allow heuristic agent.NeedsPermission uses for
// its own fixed tools.
func needsPermission(call agent.ToolCall) bool {
	if mcp.IsToolName(call.Function.Name) {
		return true
	}
	return agent.NeedsPermission(call)
}

// allowlistKey returns the key used to remember an "always allow this
// session" decision for call, and whether this call type supports one
// at all. Bash is keyed by its exact command text; MCP tools by name.
// File edits are deliberately excluded — each one is materially
// different (a different diff every time), so a blanket allow doesn't
// carry the same meaning it does for a repeated shell command or a
// repeatedly-invoked MCP tool.
func allowlistKey(call agent.ToolCall) (key string, ok bool) {
	switch {
	case call.Function.Name == "bash":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Command == "" {
			return "", false
		}
		return "bash:" + in.Command, true
	case call.Function.Name == "bash_background":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Command == "" {
			return "", false
		}
		return "bash_background:" + in.Command, true
	case mcp.IsToolName(call.Function.Name):
		return "mcp:" + call.Function.Name, true
	default:
		return "", false
	}
}

// mcpCallArgsPreview renders a truncated, pretty-printed preview of an
// MCP tool call's arguments for the permission prompt. Without this the
// prompt showed only "server: tool" with zero argument visibility —
// exactly backwards, since MCP tools are the ones gated *because*
// chisel can't reason about what an arbitrary server's tool does; not
// showing what it's being asked to do left that decision uninformed.
// Returns "" for anything that isn't an MCP call.
func mcpCallArgsPreview(call agent.ToolCall) string {
	if !mcp.IsToolName(call.Function.Name) {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(call.Function.Arguments), "", "  "); err != nil {
		return call.Function.Arguments
	}
	text := pretty.String()
	if truncated, ok := truncateRunes(text, 500); ok {
		return truncated + "\n…"
	}
	return text
}
