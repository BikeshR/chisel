//go:build integration

package tui

import (
	"os"
	"testing"
)

func TestIntegrationCheckModel(t *testing.T) {
	if os.Getenv("CHISEL_API_KEY") == "" {
		t.Skip("CHISEL_API_KEY not set — skipping integration test")
	}

	cmd := checkModel("minimax-m3")
	msg := cmd() // tea.Cmd is just a func() tea.Msg — safe to call directly in a test

	result, ok := msg.(modelCheckResultMsg)
	if !ok {
		t.Fatalf("expected modelCheckResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("checkModel failed: %v", result.err)
	}
	if result.reply == "" {
		t.Fatal("expected a non-empty reply")
	}
}

func TestIntegrationCheckModelBrokenModel(t *testing.T) {
	if os.Getenv("CHISEL_API_KEY") == "" {
		t.Skip("CHISEL_API_KEY not set — skipping integration test")
	}

	// deepseek-v4-flash failed with a generic upstream error during this
	// project's development (see docs/design.md) — confirms checkModel
	// surfaces a real failure as an error, not a false positive.
	cmd := checkModel("deepseek-v4-flash")
	msg := cmd()

	result, ok := msg.(modelCheckResultMsg)
	if !ok {
		t.Fatalf("expected modelCheckResultMsg, got %T", msg)
	}
	if result.err == nil {
		t.Skip("deepseek-v4-flash succeeded — OpenCode's known issue with it may have been resolved")
	}
	t.Logf("confirmed still failing: %v", result.err)
}
