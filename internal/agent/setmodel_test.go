package agent

import (
	"strings"
	"testing"
)

func TestSetModelPreservesToolsPlanModeAndMemory(t *testing.T) {
	c := New("minimax-m3")
	c.AddTools([]Tool{{Type: "function", Function: ToolFunction{Name: "mcp__github__list_issues"}}})
	c.SetPlanMode(true)
	c.SetMemory("this repo uses gofmt")

	c.SetModel("glm-5.2")

	if c.ModelName() != "glm-5.2" {
		t.Errorf("ModelName() = %q, want %q", c.ModelName(), "glm-5.2")
	}
	if !c.PlanMode() {
		t.Error("PlanMode() = false after SetModel, want it preserved as true")
	}

	foundMCPTool := false
	for _, tool := range c.tools {
		if tool.Function.Name == "mcp__github__list_issues" {
			foundMCPTool = true
		}
	}
	if !foundMCPTool {
		t.Error("MCP tool added via AddTools was lost after SetModel")
	}
	if c.memory != "this repo uses gofmt" {
		t.Errorf("memory = %q, want it preserved", c.memory)
	}
}

func TestRemoveToolsWithPrefix(t *testing.T) {
	c := New("minimax-m3")
	builtinCount := len(c.tools)
	c.AddTools([]Tool{
		{Type: "function", Function: ToolFunction{Name: "mcp__github__list_issues"}},
		{Type: "function", Function: ToolFunction{Name: "mcp__github__create_issue"}},
		{Type: "function", Function: ToolFunction{Name: "mcp__other__do_thing"}},
	})

	c.RemoveToolsWithPrefix("mcp__github__")

	if len(c.tools) != builtinCount+1 {
		t.Fatalf("got %d tools, want %d (built-ins + the one surviving mcp__other__ tool)", len(c.tools), builtinCount+1)
	}
	for _, tool := range c.tools {
		if strings.HasPrefix(tool.Function.Name, "mcp__github__") {
			t.Errorf("tool %q survived removal", tool.Function.Name)
		}
	}
	foundOther := false
	for _, tool := range c.tools {
		if tool.Function.Name == "mcp__other__do_thing" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Error("a tool from a different server was incorrectly removed")
	}
}

// TestRemoveToolsWithPrefixDoesNotCorruptClonedTools is the regression
// test for a real aliasing bug: RemoveToolsWithPrefix used to compact
// c.tools in place (c.tools[:0]), reusing the same backing array —
// Clone/WithoutTools do a shallow struct copy, so a clone made *before*
// RemoveToolsWithPrefix runs on the original shares that array. The old
// in-place compaction silently overwrote elements the clone's own
// (unchanged) slice length still covered, even though nothing ever
// touched clone.tools directly.
func TestRemoveToolsWithPrefixDoesNotCorruptClonedTools(t *testing.T) {
	c := New("minimax-m3")
	c.AddTools([]Tool{
		{Type: "function", Function: ToolFunction{Name: "mcp__github__list_issues"}},
		{Type: "function", Function: ToolFunction{Name: "mcp__other__do_thing"}},
	})

	clone := c.Clone("glm-5.2")
	cloneToolsBefore := append([]Tool{}, clone.tools...)

	c.RemoveToolsWithPrefix("mcp__github__")

	if len(clone.tools) != len(cloneToolsBefore) {
		t.Fatalf("clone.tools length changed from %d to %d after RemoveToolsWithPrefix ran on the original", len(cloneToolsBefore), len(clone.tools))
	}
	for i, tool := range clone.tools {
		if tool.Function.Name != cloneToolsBefore[i].Function.Name {
			t.Errorf("clone.tools[%d] = %q, want %q (unaffected by the original's RemoveToolsWithPrefix)", i, tool.Function.Name, cloneToolsBefore[i].Function.Name)
		}
	}
}

func TestSetToolsReplacesOutright(t *testing.T) {
	c := New("minimax-m3")
	if len(c.tools) == 0 {
		t.Fatal("expected New to populate built-in tools")
	}

	c.SetTools([]Tool{{Type: "function", Function: ToolFunction{Name: "only-this-one"}}})

	if len(c.tools) != 1 || c.tools[0].Function.Name != "only-this-one" {
		t.Errorf("tools = %+v, want SetTools to replace the set outright", c.tools)
	}
}
