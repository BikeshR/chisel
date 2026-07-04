//go:build integration

package tui

import (
	"context"
	"os"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestIntegrationCompact(t *testing.T) {
	if os.Getenv("CHISEL_API_KEY") == "" {
		t.Skip("CHISEL_API_KEY not set — skipping integration test")
	}

	client := agent.New("minimax-m3")
	history := []agent.Message{
		{Role: "user", Content: "My name is Alex and I'm working on a project called Zephyr. Remember that."},
		{Role: "assistant", Content: "Got it — Alex, project Zephyr."},
	}

	cmd := compact(context.Background(), client, history)
	msg := cmd()

	result, ok := msg.(compactResultMsg)
	if !ok {
		t.Fatalf("expected compactResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("compact failed: %v", result.err)
	}
	if result.summary == "" {
		t.Fatal("expected a non-empty summary")
	}
	if result.usage.InputTokens == 0 {
		t.Error("expected non-zero usage for the compaction request")
	}
	t.Logf("summary: %s", result.summary)
}
