package agent

import "testing"

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
