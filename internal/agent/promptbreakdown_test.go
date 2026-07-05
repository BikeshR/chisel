package agent

import "testing"

func TestPromptBreakdownReflectsSetMemory(t *testing.T) {
	c := New("minimax-m3")
	before := c.PromptBreakdown()

	c.SetMemory("this repo uses tabs not spaces")

	after := c.PromptBreakdown()
	if after.ProjectMemory <= before.ProjectMemory {
		t.Errorf("ProjectMemory = %d, want it to grow after SetMemory (was %d)", after.ProjectMemory, before.ProjectMemory)
	}
	if after.BaseInstructions != before.BaseInstructions {
		t.Errorf("BaseInstructions changed (%d -> %d), want it unaffected by SetMemory", before.BaseInstructions, after.BaseInstructions)
	}
}

func TestPromptBreakdownReflectsSetAgentMemory(t *testing.T) {
	c := New("minimax-m3")
	before := c.PromptBreakdown()

	c.SetAgentMemory("- remembered note")

	after := c.PromptBreakdown()
	if after.ProjectMemory <= before.ProjectMemory {
		t.Errorf("ProjectMemory = %d, want it to grow after SetAgentMemory (was %d)", after.ProjectMemory, before.ProjectMemory)
	}
}

func TestPromptBreakdownReflectsPlanMode(t *testing.T) {
	c := New("minimax-m3")
	before := c.PromptBreakdown()

	c.SetPlanMode(true)

	after := c.PromptBreakdown()
	if after.BaseInstructions <= before.BaseInstructions {
		t.Errorf("BaseInstructions = %d, want it to grow once plan mode adds planModeNote (was %d)", after.BaseInstructions, before.BaseInstructions)
	}
}

func TestPromptBreakdownReflectsToolSchemas(t *testing.T) {
	c := New("minimax-m3")
	before := c.PromptBreakdown()

	c.AddTools([]Tool{{Type: "function", Function: ToolFunction{Name: "extra_tool", Description: "an extra tool"}}})

	after := c.PromptBreakdown()
	if after.ToolSchemas <= before.ToolSchemas {
		t.Errorf("ToolSchemas = %d, want it to grow after AddTools (was %d)", after.ToolSchemas, before.ToolSchemas)
	}
}

func TestSystemPromptSectionsFullMatchesSendStreamingContent(t *testing.T) {
	c := New("minimax-m3")
	c.SetMemory("project notes")
	sections := c.systemPromptSections()

	if sections.full() != sections.base+sections.memory+sections.skills {
		t.Error("full() doesn't match its own parts concatenated")
	}
}
