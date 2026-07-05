package agent

import (
	"encoding/json"

	"github.com/BikeshR/chisel/internal/agentmemory"
)

func rememberTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "remember",
			Description: "Persist a short, durable note to this project's memory (.chisel/MEMORY.md), so it's available again in future sessions here — not just this one. Use it for things worth not re-learning: a convention this repo follows, a gotcha you hit, a preference the user stated. Don't use it for anything only relevant to the current task; that belongs in the conversation, not permanent memory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"note": map[string]any{
						"type":        "string",
						"description": "One short, self-contained note (a single line — it's stored and shown as one).",
					},
				},
				"required": []string{"note"},
			},
		},
	}
}

// runRemember validates the input and appends it to workDir's agent
// memory file — see agentmemory.Remember for the size-capping and
// atomic-write behavior.
func runRemember(workDir string, rawInput json.RawMessage) (string, error) {
	var in struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	if err := agentmemory.Remember(workDir, in.Note); err != nil {
		return "", err
	}
	return "remembered", nil
}
