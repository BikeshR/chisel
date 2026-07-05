package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

func oracleTool() Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "consult_oracle",
			Description: "Ask a hard question — a tricky bug, a design decision, a second opinion on your current approach — to a fresh, independent instance of the model with no access to this conversation's history. Uses the planner model if one is configured (/model planner) for a genuinely different perspective; otherwise a fresh instance of the same model, still useful for an uncluttered read unclouded by everything discussed so far. It can explore the codebase read-only (glob, grep, view) to ground its answer, but describe the situation fully in the question — it starts knowing nothing else about it. Doesn't touch any files or run anything.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{
						"type":        "string",
						"description": "A complete, self-contained description of the problem or decision, plus what's already been considered.",
					},
				},
				"required": []string{"question"},
			},
		},
	}
}

// runConsultOracle validates the input, picks which model actually
// consults (plannerModel if one is configured, otherwise the same
// model the caller is already using — see oracleTool's own doc
// comment), and runs it.
func runConsultOracle(ctx context.Context, workDir, model, plannerModel string, rawInput json.RawMessage) (string, Usage, error) {
	var in struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", Usage{}, err
	}
	if in.Question == "" {
		return "", Usage{}, fmt.Errorf("question is required")
	}
	oracleModel := plannerModel
	if oracleModel == "" {
		oracleModel = model
	}
	return RunOracle(ctx, workDir, oracleModel, in.Question)
}

// RunOracle runs a self-contained conversation using model, seeded
// with question as the sole starting message and restricted to
// subagentTools() (glob/grep/view) — no permission gate needed, same
// reasoning as RunSubagent, since nothing in that tool set can mutate
// anything. Deliberately doesn't offer consult_oracle or
// dispatch_subagent to the oracle itself: it's meant to give one
// grounded, reasoned answer directly, not delegate further.
func RunOracle(ctx context.Context, workDir, model, question string) (string, Usage, error) {
	client := New(model)
	client.tools = subagentTools()

	history := []Message{{Role: "user", Content: oracleQuestionPrompt(question)}}
	return RunLoop(ctx, client, history, maxSubagentTurns, func(call ToolCall) ToolResult {
		return Execute(ctx, workDir, model, call, nil, nil, nil, "") // subagentTools never dispatches to bash, remember, dispatch_subagent, or consult_oracle itself
	}, nil)
}

func oracleQuestionPrompt(question string) string {
	return "You are a reasoning consultant giving a second opinion on a hard problem — a tricky bug, a design decision, an architectural tradeoff. You can explore the codebase read-only (glob, grep, view) to ground your answer in the actual code, but you have no access to the conversation that led here, so the question below is everything you know about the situation. Give a direct, reasoned answer, not a list of clarifying questions back:\n\n" + question
}
