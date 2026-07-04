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

func runDispatchSubagent(ctx context.Context, workDir, model string, rawInput json.RawMessage) (string, Usage, error) {
	var in struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", Usage{}, err
	}
	if in.Task == "" {
		return "", Usage{}, fmt.Errorf("task is required")
	}
	return RunSubagent(ctx, workDir, model, in.Task)
}

// RunSubagent runs a self-contained conversation using model, seeded with
// task as the sole starting message and restricted to subagentTools() —
// executed directly with no permission gate, since nothing in that tool
// set can mutate anything. Returns the subagent's final answer and its
// accumulated token usage across every turn it took — without plumbing
// this back, a subagent's real cost (often the most expensive kind of
// call here, being multi-turn on its own) was invisible to the parent's
// token accounting, undercounting exactly when spend was highest.
func RunSubagent(ctx context.Context, workDir, model, task string) (string, Usage, error) {
	client := New(model)
	client.tools = subagentTools()

	history := []Message{{Role: "user", Content: subagentTaskPrompt(task)}}
	var total Usage

	for turn := 1; turn <= maxSubagentTurns; turn++ {
		ch, err := client.SendStreaming(ctx, history)
		if err != nil {
			return "", total, fmt.Errorf("subagent turn %d: %w", turn, err)
		}

		msg, usage, err := Drain(ch)
		if err != nil {
			return "", total, fmt.Errorf("subagent turn %d: %w", turn, err)
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens

		history = append(history, *msg)

		// Go by whether the message actually has tool calls, not
		// finish_reason — see the same fix and reasoning in
		// internal/tui/update.go's handleStreamComplete.
		if len(msg.ToolCalls) == 0 {
			return msg.Content, total, nil
		}

		for _, call := range msg.ToolCalls {
			result := Execute(ctx, workDir, model, call, nil) // subagentTools never dispatches to bash
			history = append(history, result.ToMessage())
		}
	}

	return "", total, fmt.Errorf("subagent did not finish within %d turns", maxSubagentTurns)
}

func subagentTaskPrompt(task string) string {
	return "You are a read-only research subagent: you can explore (glob, grep, view) but can't edit files, run commands, or dispatch further subagents, and there's no one to answer follow-up questions. Complete this task and give one concise, final answer:\n\n" + task
}
