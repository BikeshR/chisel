package agent

import (
	"encoding/json"
	"fmt"
)

// TodoItem is one entry in a model-maintained task checklist — see
// updateTodosTool. Exported so internal/tui can extract the current
// list directly from a tool call's own arguments and render it live;
// this tool's result content sent back to the model is just a short
// confirmation string, not the structured list itself, so this package
// stays free of any UI-rendering concerns.
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // "pending" | "in_progress" | "completed"
}

func updateTodosTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "update_todos",
			Description: "Replace the current task checklist, shown live to the user. Use it for any task with more than a couple of steps, to track progress and stay on plan — not for a single trivial action. Mark exactly one item \"in_progress\" at a time (whichever you're currently doing); mark items \"completed\" as soon as they're actually done, not in a batch at the end. Always send the complete list, not just what changed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{
									"type":        "string",
									"description": "Short description of the task",
								},
								"status": map[string]any{
									"type": "string",
									"enum": []string{"pending", "in_progress", "completed"},
								},
							},
							"required": []string{"content", "status"},
						},
					},
				},
				"required": []string{"todos"},
			},
		},
	}
}

// runUpdateTodos validates the input and returns a short confirmation.
// The structured list itself isn't needed here — internal/tui extracts
// it directly from the tool call's own arguments (see model.go's
// executeTool) to render live, rather than this function threading it
// through ToolResult.
func runUpdateTodos(rawInput json.RawMessage) (string, error) {
	var in struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	for _, item := range in.Todos {
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("invalid status %q — must be pending, in_progress, or completed", item.Status)
		}
	}
	return fmt.Sprintf("todo list updated (%d items)", len(in.Todos)), nil
}
