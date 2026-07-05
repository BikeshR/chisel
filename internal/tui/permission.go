package tui

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/permrules"
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
// a tool call about to be dispatched. Persistent rules (rules,
// .chisel/permissions.json — see internal/permrules) are checked
// first: a "deny" rule always wins, even over a call that would
// otherwise be auto-allowed, since making something *more* restrictive
// never conflicts with anything else here. An "allow" rule is checked
// next, but still loses to plan mode, same as the in-memory
// session-only allowlist below it — plan mode is meant to be an
// absolute guarantee that nothing runs, so a rule (or a previous
// always-allow decision) must still be blocked while it's on, not
// silently exempted from it.
func decidePermission(call agent.ToolCall, mode agent.Mode, allowlist map[string]bool, rules permrules.Config) (decision permissionDecision, reason string) {
	needsConfirmation := needsPermission(call)

	if ruleDecision, matched := matchPermissionRules(rules, call); matched {
		if ruleDecision == permrules.Deny {
			return permissionDeny, fmt.Sprintf("Not run — a rule in .chisel/permissions.json denies %s.", summarizeCall(call))
		}
		if !needsConfirmation || mode != agent.ModePlan {
			return permissionAllow, ""
		}
	}

	if needsConfirmation && mode == agent.ModePlan {
		return permissionDeny, "Not run — chisel is in plan mode, which only allows read-only exploration. Describe this as part of your plan instead, then stop; the user will exit plan mode before you make any changes."
	}
	// Accept-edits mode auto-approves only chisel's own known, path-
	// validated (resolveInWorkDir) editor tool — never bash or an MCP
	// tool, which chisel can't reason about the effect of the way it can
	// a diffed file edit. This is deliberately narrower than an
	// auto-approve-everything mode: chisel has no bash sandbox, so
	// blanket auto-approval of arbitrary shell commands stays exactly as
	// gated as it's always been (see docs/design.md's own reasoning for
	// why sandboxing is a prerequisite for that, not this).
	if needsConfirmation && mode == agent.ModeAcceptEdits && isEditCall(call) {
		return permissionAllow, ""
	}
	if needsConfirmation {
		if key, ok := allowlistKey(call); ok && allowlist[key] {
			return permissionAllow, ""
		}
		return permissionAsk, ""
	}
	return permissionAllow, ""
}

// isEditCall reports whether call is chisel's own file-editing tool —
// the only call type accept-edits mode auto-approves. Doesn't need to
// re-check which specific edit subcommand it is: decidePermission only
// calls this when needsPermission(call) is already true, and the editor
// tool's "view" subcommand never needs permission in the first place
// (see agent.NeedsPermission), so reaching here already means it's a
// mutating one.
func isEditCall(call agent.ToolCall) bool {
	return call.Function.Name == "str_replace_based_edit_tool"
}

// matchPermissionRules extracts the text a rule's pattern should match
// against for call, and checks it against rules — bash/bash_background
// against the command text itself, str_replace_based_edit_tool against
// its path (a file path is just as natural a single string to glob-match
// as a bash command is — "allow edits under src/**" reads the same way
// "allow git *" already does). Any other tool has no natural single
// string to match a shell-style glob against, so it's left to the
// normal allow/ask/deny path.
func matchPermissionRules(rules permrules.Config, call agent.ToolCall) (permrules.Decision, bool) {
	switch call.Function.Name {
	case "bash", "bash_background":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Command == "" {
			return "", false
		}
		return permrules.Match(rules, call.Function.Name, in.Command)
	case "str_replace_based_edit_tool":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Path == "" {
			return "", false
		}
		return permrules.Match(rules, call.Function.Name, in.Path)
	default:
		return "", false
	}
}

// doomLoopThreshold is how many identical calls in a row (see
// toolCallKey) force a confirmation regardless of what decidePermission
// would otherwise say — including a call that's auto-allowed by
// default or already on the "always allow" list. A model that's stuck
// re-issuing the exact same call over and over is a real, observed
// tool-calling failure mode (OpenCode calls this "doom loop" detection)
// rather than a hypothetical one, and it's cheap insurance for chisel
// specifically: OpenCode Go's hard per-window dollar caps (see
// docs/design.md §4) mean a runaway loop doesn't just waste time, it
// can burn through a session's budget outright.
const doomLoopThreshold = 3

// toolCallKey identifies a call by name and exact arguments, for
// detecting repetition — see doomLoopThreshold. Deliberately coarser
// than allowlistKey (which only covers bash/MCP): any tool repeated
// identically is worth noticing, not just the ones eligible for
// always-allow.
func toolCallKey(call agent.ToolCall) string {
	return call.Function.Name + "\x00" + call.Function.Arguments
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

// persistableRuleFor returns the tool name and glob pattern
// permrules.Add would use to permanently allow call going forward (see
// the "p" prompt option in update.go), and whether call is eligible at
// all. Deliberately matches exactly what matchPermissionRules above
// already supports — bash/bash_background command text, and
// str_replace_based_edit_tool's path — since a persistent rule for
// anything else (an MCP tool's own arguments, say) has no natural
// single string to write a shell-style glob against.
func persistableRuleFor(call agent.ToolCall) (toolName, pattern string, ok bool) {
	switch call.Function.Name {
	case "bash", "bash_background":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Command == "" {
			return "", "", false
		}
		return call.Function.Name, in.Command, true
	case "str_replace_based_edit_tool":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Path == "" {
			return "", "", false
		}
		return call.Function.Name, in.Path, true
	default:
		return "", "", false
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
