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

// ReadOnlyTools is subagentTools, exported for headless mode (chisel
// -p): a non-interactive invocation has no terminal to show a
// permission prompt to, so it needs the same guarantee a subagent
// relies on — a tool set that's incapable of mutating anything, by
// construction, rather than one that merely asks first.
func ReadOnlyTools() []Tool {
	return subagentTools()
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
	return RunLoop(ctx, client, history, maxSubagentTurns, func(call ToolCall) ToolResult {
		return Execute(ctx, workDir, model, call, nil) // subagentTools never dispatches to bash
	})
}

// RunLoop runs a synchronous send/dispatch loop: send history to
// client, and for each tool call the model makes in response, invoke
// execTool and feed its result back, until the model responds with no
// more tool calls or maxTurns is exceeded. Returns the model's final
// answer and its accumulated token usage across every turn. Shared by
// RunSubagent (execTool restricted to a fixed, read-only tool set with
// no permission gate) and headless mode (chisel -p, in main.go), which
// supplies its own tool set and dispatch function instead.
func RunLoop(ctx context.Context, client *Client, history []Message, maxTurns int, execTool func(ToolCall) ToolResult) (string, Usage, error) {
	var total Usage

	for turn := 1; turn <= maxTurns; turn++ {
		ch, err := client.SendStreaming(ctx, history)
		if err != nil {
			return "", total, fmt.Errorf("turn %d: %w", turn, err)
		}

		msg, usage, err := Drain(ch)
		if err != nil {
			return "", total, fmt.Errorf("turn %d: %w", turn, err)
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
			result := execTool(call)
			history = append(history, result.ToMessage())
		}
	}

	return "", total, fmt.Errorf("did not finish within %d turns", maxTurns)
}

func subagentTaskPrompt(task string) string {
	return "You are a read-only research subagent: you can explore (glob, grep, view) but can't edit files, run commands, or dispatch further subagents, and there's no one to answer follow-up questions. Complete this task and give one concise, final answer:\n\n" + task
}
