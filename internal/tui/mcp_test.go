package tui

import (
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestSummarizeCallMCPTool(t *testing.T) {
	call := agent.ToolCall{
		Function: agent.ToolCallFunction{Name: "mcp__github__list_issues", Arguments: "{}"},
	}
	if got, want := summarizeCall(call), "github: list_issues"; got != want {
		t.Errorf("summarizeCall = %q, want %q", got, want)
	}
}

func TestSummarizeCallBuiltinToolFallsThrough(t *testing.T) {
	call := agent.ToolCall{
		Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`},
	}
	if got, want := summarizeCall(call), "run: ls"; got != want {
		t.Errorf("summarizeCall = %q, want %q (agent.Summarize's own rendering)", got, want)
	}
}

func TestNeedsPermissionMCPToolsAlwaysTrue(t *testing.T) {
	// Even a hypothetical read-only-sounding MCP tool must ask — chisel
	// can't know what an arbitrary server's tool actually does.
	call := agent.ToolCall{
		Function: agent.ToolCallFunction{Name: "mcp__github__list_issues", Arguments: "{}"},
	}
	if !needsPermission(call) {
		t.Error("needsPermission = false for an MCP tool, want true always")
	}
}

func TestNeedsPermissionBuiltinToolsFallThrough(t *testing.T) {
	readOnly := agent.ToolCall{Function: agent.ToolCallFunction{Name: "glob", Arguments: "{}"}}
	if needsPermission(readOnly) {
		t.Error("needsPermission = true for glob, want false (matches agent.NeedsPermission)")
	}

	writes := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: "{}"}}
	if !needsPermission(writes) {
		t.Error("needsPermission = false for bash, want true (matches agent.NeedsPermission)")
	}
}
