package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleContextCommandShowsBreakdownAndActualTotal(t *testing.T) {
	m := Model{
		client:            agent.New("minimax-m3"),
		messages:          []agent.Message{{Role: "user", Content: "hello"}},
		lastContextTokens: 1234,
	}
	got := m.handleContextCommand()
	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want a single entry", lines)
	}
	text := lines[0]

	for _, want := range []string{"base instructions", "project memory", "skills", "tool schemas", "transcript", "1.2k", "estimate"} {
		if !strings.Contains(text, want) {
			t.Errorf("context output missing %q: %q", want, text)
		}
	}
}

// TestHandleContextCommandDoesNotClaimExactness guards against
// silently regressing into overclaiming precision chisel can't back
// up — chisel has no real tokenizer, so every per-category number here
// must read as an estimate, not a fact.
func TestHandleContextCommandDoesNotClaimExactness(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got := m.handleContextCommand()
	text := strings.Join(got.renderedLines(), "\n")

	if !strings.Contains(text, "~") {
		t.Error("expected estimated figures to be marked with ~, not presented as exact")
	}
	if !strings.Contains(strings.ToLower(text), "estimate") {
		t.Error("expected the output to explicitly call itself an estimate somewhere")
	}
}
