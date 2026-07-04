package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// maxSubagentTurns bounds a subagent's own tool-calling loop — a safety
// net against a back-and-forth that never settles, in the same spirit as
// BashSession's per-command timeout.
const maxSubagentTurns = 15

// subagentTools is deliberately narrower than buildTools(): glob, grep,
// and view — a read-only-only variant of the editor tool, with no
// create/str_replace/insert command available at all, not just
// discouraged by the prompt. A subagent runs with no permission gate —
// its tool set has to be incapable of mutating anything by construction,
// not by the model choosing to behave.
func subagentTools() []Tool {
	return []Tool{globTool(), grepTool(), viewTool()}
}

func viewTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "view",
			Description: "View a file (optionally a [start,end] line range) or list a directory's contents. Read-only.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path relative to the working directory",
					},
					"view_range": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer"},
						"description": "Optional [start, end] line range (end -1 means end of file)",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

func runView(workDir string, rawInput json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		ViewRange []int  `json:"view_range"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	path, err := resolveInWorkDir(workDir, in.Path)
	if err != nil {
		return "", err
	}
	return viewPath(path, in.ViewRange)
}

func runDispatchSubagent(ctx context.Context, workDir, model string, rawInput json.RawMessage) (string, error) {
	var in struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	if in.Task == "" {
		return "", fmt.Errorf("task is required")
	}
	return RunSubagent(ctx, workDir, model, in.Task)
}

// RunSubagent runs a self-contained conversation using model, seeded with
// task as the sole starting message and restricted to subagentTools() —
// executed directly with no permission gate, since nothing in that tool
// set can mutate anything. Returns the subagent's final answer.
func RunSubagent(ctx context.Context, workDir, model, task string) (string, error) {
	client := New(model)
	client.tools = subagentTools()

	history := []Message{{Role: "user", Content: subagentTaskPrompt(task)}}

	for turn := 1; turn <= maxSubagentTurns; turn++ {
		ch, err := client.SendStreaming(ctx, history)
		if err != nil {
			return "", fmt.Errorf("subagent turn %d: %w", turn, err)
		}

		msg, _, err := Drain(ch)
		if err != nil {
			return "", fmt.Errorf("subagent turn %d: %w", turn, err)
		}

		history = append(history, *msg)

		// Go by whether the message actually has tool calls, not
		// finish_reason — see the same fix and reasoning in
		// internal/tui/update.go's handleStreamComplete.
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, call := range msg.ToolCalls {
			result := Execute(ctx, workDir, model, call, nil) // subagentTools never dispatches to bash
			history = append(history, result.ToMessage())
		}
	}

	return "", fmt.Errorf("subagent did not finish within %d turns", maxSubagentTurns)
}

func subagentTaskPrompt(task string) string {
	return "You are a read-only research subagent: you can explore (glob, grep, view) but can't edit files, run commands, or dispatch further subagents, and there's no one to answer follow-up questions. Complete this task and give one concise, final answer:\n\n" + task
}
