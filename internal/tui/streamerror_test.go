package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestHandleStreamEventGenuineErrorHintsRetry(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, _ := m.handleStreamEvent(streamEventMsg{event: agent.Event{Done: true, Err: errors.New("upstream request failed")}})
	gotModel := got.(Model)

	lines := gotModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "upstream request failed") && strings.Contains(l, "/retry") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the error line to hint /retry", lines)
	}
}

func TestHandleStreamEventInterruptionHasNoRetryHint(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, _ := m.handleStreamEvent(streamEventMsg{event: agent.Event{Done: true, Err: context.Canceled}})
	gotModel := got.(Model)

	lines := gotModel.renderedLines()
	for _, l := range lines {
		if strings.Contains(l, "/retry") {
			t.Errorf("lines = %+v, want no /retry hint for a user-initiated interruption", lines)
		}
	}
}
