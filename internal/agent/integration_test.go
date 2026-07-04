//go:build integration

// Real, live-network tests against OpenCode Go — replaces the throwaway
// .smoketest/main.go scripts used during chisel's early development. Run
// with:
//
//	go test -tags=integration ./internal/agent/...
//
// Needs CHISEL_API_KEY set (~/.chisel.env is not read here — export it
// directly, or `source <(grep CHISEL_ ~/.chisel.env)` first).
package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

func testClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("CHISEL_API_KEY") == "" {
		t.Skip("CHISEL_API_KEY not set — skipping integration test")
	}
	return New("minimax-m3")
}

func drainToFinal(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	var final Event
	for ev := range ch {
		if ev.Done {
			final = ev
		}
	}
	return final
}

func TestIntegrationPlainChat(t *testing.T) {
	client := testClient(t)

	ch, err := client.SendStreaming(context.Background(), []Message{
		{Role: "user", Content: "Reply with exactly the words: it works"},
	})
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}

	final := drainToFinal(t, ch)
	if final.Err != nil {
		t.Fatalf("stream error: %v", final.Err)
	}
	if final.Message == nil || final.Message.Content == "" {
		t.Fatal("expected a non-empty response")
	}
	if final.FinishReason == "" {
		t.Error("expected a finish reason")
	}
	if final.Usage.InputTokens == 0 && final.Usage.OutputTokens == 0 {
		t.Error("expected non-zero token usage")
	}
}

func TestIntegrationToolCalling(t *testing.T) {
	client := testClient(t)

	history := []Message{
		{Role: "user", Content: "Use the glob tool to find all .go files. Just call the tool, no explanation."},
	}

	ch, err := client.SendStreaming(context.Background(), history)
	if err != nil {
		t.Fatalf("SendStreaming: %v", err)
	}
	final := drainToFinal(t, ch)
	if final.Err != nil {
		t.Fatalf("stream error: %v", final.Err)
	}
	if len(final.Message.ToolCalls) != 1 {
		t.Fatalf("expected exactly one tool call, got %d: %+v", len(final.Message.ToolCalls), final.Message.ToolCalls)
	}
	tc := final.Message.ToolCalls[0]
	if tc.Function.Name != "glob" {
		t.Fatalf("expected the glob tool, got %q", tc.Function.Name)
	}

	result := Execute(context.Background(), ".", "", tc, nil) // glob never touches the bash session or needs a model
	if result.IsError {
		t.Fatalf("tool execution failed: %s", result.Content)
	}

	// Full round trip: send the tool result back and confirm the model
	// produces a real follow-up rather than erroring or going silent.
	history = append(history, *final.Message, result.ToMessage())
	ch2, err := client.SendStreaming(context.Background(), history)
	if err != nil {
		t.Fatalf("SendStreaming (turn 2): %v", err)
	}
	final2 := drainToFinal(t, ch2)
	if final2.Err != nil {
		t.Fatalf("stream error (turn 2): %v", final2.Err)
	}
	if final2.Message == nil || final2.Message.Content == "" {
		t.Fatal("expected a non-empty follow-up response")
	}
}

func TestIntegrationRunSubagent(t *testing.T) {
	if os.Getenv("CHISEL_API_KEY") == "" {
		t.Skip("CHISEL_API_KEY not set — skipping integration test")
	}

	summary, usage, err := RunSubagent(context.Background(), ".", "minimax-m3",
		"Find where the BashSession type is defined in this Go codebase and say which file it's in.")
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if summary == "" {
		t.Fatal("expected a non-empty summary")
	}
	if !strings.Contains(summary, "bashsession.go") {
		t.Errorf("summary = %q, want it to correctly identify bashsession.go", summary)
	}
	if usage.InputTokens == 0 {
		t.Error("expected non-zero usage plumbed back from a real subagent run")
	}
}
