package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/BikeshR/chisel/internal/subagentdef"
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

func runDispatchSubagent(ctx context.Context, workDir, model string, rawInput json.RawMessage, subagents map[string]subagentdef.Subagent) (string, Usage, error) {
	var in struct {
		Task  string `json:"task"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", Usage{}, err
	}
	if in.Task == "" {
		return "", Usage{}, fmt.Errorf("task is required")
	}
	var rolePrompt string
	if in.Agent != "" {
		def, ok := subagents[in.Agent]
		if !ok {
			return "", Usage{}, fmt.Errorf("no subagent role named %q is defined", in.Agent)
		}
		rolePrompt = def.Prompt
	}
	return RunSubagent(ctx, workDir, model, in.Task, rolePrompt)
}

// RunSubagent runs a self-contained conversation using model, seeded with
// task (plus rolePrompt, if a custom subagent role was requested — see
// subagentTaskPrompt) as the sole starting message, restricted to
// subagentTools() — executed directly with no permission gate, since
// nothing in that tool set can mutate anything. rolePrompt only ever
// adds instructions layered on top of the task; it can't widen the tool
// set a role runs with, which is what keeps every custom subagent role
// exempt from the permission gate the same way the built-in one is.
// Returns the subagent's final answer and its accumulated token usage
// across every turn it took — without plumbing this back, a subagent's
// real cost (often the most expensive kind of call here, being
// multi-turn on its own) was invisible to the parent's token
// accounting, undercounting exactly when spend was highest.
func RunSubagent(ctx context.Context, workDir, model, task, rolePrompt string) (string, Usage, error) {
	client := New(model)
	client.tools = subagentTools()

	history := []Message{{Role: "user", Content: subagentTaskPrompt(task, rolePrompt)}}
	return RunLoop(ctx, client, history, maxSubagentTurns, func(call ToolCall) ToolResult {
		return Execute(ctx, workDir, model, call, nil, nil, nil) // subagentTools never dispatches to bash, load_skill, remember, or dispatch_subagent
	}, nil)
}

// LoopEvent is one tool call's progress within RunLoop, reported via
// its optional onEvent callback — chisel -p -json-stream's NDJSON mode
// is the only caller that supplies one today (see main.go), so
// CI/scripting output can show tool calls as they happen instead of
// only a single blob once the whole run is done. Phase is "start"
// (right before execTool runs) or "end" (right after); Result/IsError
// are only meaningful for "end".
type LoopEvent struct {
	Phase   string
	Tool    string
	Result  string
	IsError bool
}

// RunLoop runs a synchronous send/dispatch loop: send history to
// client, and for each tool call the model makes in response, invoke
// execTool and feed its result back, until the model responds with no
// more tool calls or maxTurns is exceeded. Returns the model's final
// answer and its accumulated token usage across every turn. Shared by
// RunSubagent (execTool restricted to a fixed, read-only tool set with
// no permission gate) and headless mode (chisel -p, in main.go), which
// supplies its own tool set and dispatch function instead. onEvent is
// called around every tool call if non-nil — nil is fine and the
// common case (RunSubagent has nothing to report progress to).
//
// Every call is checked against client.tools — the exact schema sent in
// the request — before reaching execTool. Both callers rely on "this
// tool set can't mutate anything" being a guarantee enforced by
// construction, not by the model's cooperation (see subagentTools'/
// ReadOnlyTools' own doc comments); without this check, execTool itself
// (agent.Execute, dispatching purely by call.Function.Name) would still
// run a real edit or an unbounded dispatch_subagent recursion for any
// hallucinated call whose name happens to match a tool that exists in
// the package but was never offered — a str_replace_based_edit_tool
// call is exactly the kind of thing every coding model has seen
// thousands of times in training, offered or not.
func RunLoop(ctx context.Context, client *Client, history []Message, maxTurns int, execTool func(ToolCall) ToolResult, onEvent func(LoopEvent)) (string, Usage, error) {
	var total Usage
	offered := toolNameSet(client.tools)

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
			if onEvent != nil {
				onEvent(LoopEvent{Phase: "start", Tool: call.Function.Name})
			}
			var result ToolResult
			if !offered[call.Function.Name] {
				result = ToolResult{
					ID:      call.ID,
					Content: fmt.Sprintf("%q was not offered in this request and cannot be called here", call.Function.Name),
					IsError: true,
				}
			} else {
				result = execTool(call)
			}
			if onEvent != nil {
				onEvent(LoopEvent{Phase: "end", Tool: call.Function.Name, Result: result.Content, IsError: result.IsError})
			}
			history = append(history, result.ToMessage())
		}
	}

	return "", total, fmt.Errorf("did not finish within %d turns", maxTurns)
}

// toolNameSet is the set of tool names actually offered in a request —
// the whitelist RunLoop checks every call against before it ever
// reaches execTool.
func toolNameSet(tools []Tool) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Function.Name] = true
	}
	return names
}

func subagentTaskPrompt(task, rolePrompt string) string {
	base := "You are a read-only research subagent: you can explore (glob, grep, view) but can't edit files, run commands, or dispatch further subagents, and there's no one to answer follow-up questions."
	if rolePrompt != "" {
		base += "\n\n" + rolePrompt
	}
	return base + " Complete this task and give one concise, final answer:\n\n" + task
}
