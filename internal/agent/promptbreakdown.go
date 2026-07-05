package agent

import "encoding/json"

// promptSections is the system prompt split into the same pieces
// SendStreaming assembles it from — factored out so PromptBreakdown can
// measure exactly what actually gets sent, rather than duplicating (and
// risking drifting from) SendStreaming's own concatenation.
type promptSections struct {
	base   string // the fixed systemPrompt, plus planModeNote if plan mode is on
	memory string // CHISEL.md/AGENTS.md plus the project's remembered agent memory, if either is set
	skills string // the "available skills" section, if any skills were loaded
}

func (s promptSections) full() string {
	return s.base + s.memory + s.skills
}

func (c *Client) systemPromptSections() promptSections {
	base := systemPrompt
	if c.mode == ModePlan {
		base += planModeNote
	}

	var memory string
	if c.memory != "" {
		memory += "\n\n---\n\nProject and user instructions:\n\n" + c.memory
	}
	if c.agentMemory != "" {
		memory += "\n\n---\n\nNotes you saved to this project's memory in a past session (via the remember tool):\n\n" + c.agentMemory
	}

	var skills string
	if c.skillsPrompt != "" {
		skills = "\n\n---\n\n" + c.skillsPrompt
	}

	return promptSections{base: base, memory: memory, skills: skills}
}

// PromptBreakdown is a rough, character-count-based estimate of how the
// next request's payload divides across categories — deliberately not
// a real token count. chisel has no tokenizer and, on principle,
// doesn't maintain a per-model context-window table either (see
// docs/design.md: getting a specific model's exact limit wrong would be
// worse than not claiming one at all) — the same caution applies here,
// so these are byte counts, for /context to convert to a rough
// "~N tok" estimate and clearly label as approximate, not an
// authoritative figure. ToolSchemas is JSON-marshaled since that's the
// actual shape sent over the wire (see chatRequest); a marshal failure
// (only possible for a pathological, non-marshalable Tool value, never
// hit in practice) leaves it at 0 rather than failing the whole
// breakdown.
type PromptBreakdown struct {
	BaseInstructions int
	ProjectMemory    int
	Skills           int
	ToolSchemas      int
}

func (c *Client) PromptBreakdown() PromptBreakdown {
	s := c.systemPromptSections()
	toolsJSON, _ := json.Marshal(c.tools)
	return PromptBreakdown{
		BaseInstructions: len(s.base),
		ProjectMemory:    len(s.memory),
		Skills:           len(s.skills),
		ToolSchemas:      len(toolsJSON),
	}
}
