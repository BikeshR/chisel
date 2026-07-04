package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
)

func TestNewShowsWarningWhenSessionLoadFailed(t *testing.T) {
	client := agent.New("minimax-m3")
	m := New(client, t.TempDir(), nil, &mcp.Registry{}, hooks.Config{}, false, false, nil, nil, nil, nil, nil, time.Time{}, true, "test-session-id")

	found := false
	for _, line := range m.renderedLines() {
		if strings.Contains(line, "couldn't be read") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning line when sessionLoadFailed is true")
	}
}

func TestNewNoWarningOnCleanStart(t *testing.T) {
	client := agent.New("minimax-m3")
	m := New(client, t.TempDir(), nil, &mcp.Registry{}, hooks.Config{}, false, false, nil, nil, nil, nil, nil, time.Time{}, false, "test-session-id")

	for _, line := range m.renderedLines() {
		if strings.Contains(line, "couldn't be read") {
			t.Error("expected no session-load warning when sessionLoadFailed is false")
		}
	}
}
