package agent

import (
	"encoding/json"
	"fmt"

	"github.com/BikeshR/chisel/internal/skill"
)

func loadSkillTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "load_skill",
			Description: "Load the full instructions for a named skill, listed by name and description in the system prompt. Call this when a skill's description matches what you're currently doing — not preemptively, and not for skills that aren't relevant to the current task.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The skill's name, exactly as listed in the system prompt",
					},
				},
				"required": []string{"name"},
			},
		},
	}
}

// runLoadSkill looks up name in skills and returns its full content —
// the only place a skill's Content ever reaches the model, keeping an
// unused skill's cost to just one line in the system prompt.
func runLoadSkill(skills map[string]skill.Skill, rawInput json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	s, ok := skills[in.Name]
	if !ok {
		return "", fmt.Errorf("no skill named %q", in.Name)
	}
	return s.Content, nil
}
