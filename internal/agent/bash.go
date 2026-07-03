package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
)

type bashInput struct {
	Command string `json:"command"`
	Restart bool   `json:"restart"`
}

// runBash executes a single shell command in workDir and returns combined
// stdout+stderr. Each call is a fresh subprocess — chisel doesn't keep a
// persistent shell session between calls yet, so state like `cd` or
// exported variables doesn't carry over from one bash call to the next.
func runBash(ctx context.Context, workDir string, rawInput json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", err
	}
	if in.Restart {
		return "no persistent session to restart", nil
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	cmd.Dir = workDir

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	output := out.String()
	if runErr != nil {
		if output != "" {
			output += "\n"
		}
		output += runErr.Error()
	}
	if output == "" {
		output = "(no output)"
	}
	return output, nil
}
