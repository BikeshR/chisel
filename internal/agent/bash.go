package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

type bashInput struct {
	Command string `json:"command"`
	Restart bool   `json:"restart"`
}

// runBash runs a command in the given persistent session — see
// BashSession for what "persistent" means and its limitations.
func runBash(ctx context.Context, session *BashSession, rawInput json.RawMessage) (string, error) {
	if session == nil {
		// Reachable if a caller ever offers the model a "bash" tool
		// without a real session behind it (today, only subagents do
		// this, and subagentTools() never advertises "bash" in the first
		// place — but that's a convention enforced by what tools get
		// offered, not by anything here). A nil-pointer panic inside
		// BashSession.Run would be a much worse failure mode than a plain
		// error for what's ultimately just a misconfigured caller.
		return "", fmt.Errorf("bash tool called with no session available")
	}
	var in bashInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	return session.Run(ctx, in.Command, in.Restart)
}
