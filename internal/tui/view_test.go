package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestStatusLine(t *testing.T) {
	m := Model{
		client:            agent.New("minimax-m3"),
		lastContextTokens: 12400,
		tokensIn:          45200,
		tokensOut:         8100,
	}
	line := m.statusLine()

	if !strings.Contains(line, "minimax-m3") {
		t.Errorf("status line = %q, want the model name", line)
	}
	if !strings.Contains(line, "12.4k") {
		t.Errorf("status line = %q, want the current context size", line)
	}
	if !strings.Contains(line, "45.2k") || !strings.Contains(line, "8.1k") {
		t.Errorf("status line = %q, want cumulative spend", line)
	}
	if strings.Contains(line, "consider /compact") {
		t.Error("status line suggests /compact below the warning threshold")
	}
}

func TestStatusLineWarnsPastThreshold(t *testing.T) {
	m := Model{
		client:            agent.New("minimax-m3"),
		lastContextTokens: contextWarnThreshold + 1,
	}
	line := m.statusLine()
	if !strings.Contains(line, "consider /compact") {
		t.Errorf("status line = %q, want a /compact suggestion past the threshold", line)
	}
}
