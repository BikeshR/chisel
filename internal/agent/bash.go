package agent

import (
	"context"
	"encoding/json"
)

type bashInput struct {
	Command string `json:"command"`
	Restart bool   `json:"restart"`
}

// runBash runs a command in the given persistent session — see
// BashSession for what "persistent" means and its limitations.
func runBash(ctx context.Context, session *BashSession, rawInput json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	return session.Run(ctx, in.Command, in.Restart)
}
