package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestNewTurnContextCancelable(t *testing.T) {
	var m Model
	ctx := m.newTurnContext()
	if ctx.Err() != nil {
		t.Fatal("freshly created context is already done")
	}
	m.cancelTurn()
	if ctx.Err() == nil {
		t.Fatal("context is not done after calling its cancel func")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
}

func TestNewTurnContextCancelsPriorTurn(t *testing.T) {
	var m Model
	first := m.newTurnContext()
	second := m.newTurnContext() // starting a new turn cancels any still-running prior one
	_ = second

	if first.Err() == nil {
		t.Error("starting a second turn should cancel the first turn's context")
	}
}

func TestEndTurnClearsCancelFunc(t *testing.T) {
	m := Model{}
	m.newTurnContext()
	if m.cancelTurn == nil {
		t.Fatal("cancelTurn not set after newTurnContext")
	}
	m.endTurn()
	if m.cancelTurn != nil {
		t.Error("cancelTurn not cleared after endTurn")
	}
}

func TestEscCancelsWhileWaitingOnModel(t *testing.T) {
	m := Model{state: stateWaitingModel}
	ctx := m.newTurnContext()

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("expected a nil Cmd — esc just cancels, it doesn't itself change state")
	}
	gotModel := got.(Model)
	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want unchanged (stateWaitingModel) — the cancelled operation's own error path changes state", gotModel.state)
	}
	if ctx.Err() == nil {
		t.Fatal("esc during stateWaitingModel did not cancel the in-flight context")
	}
}

func TestEscCancelsWhileExecutingTool(t *testing.T) {
	m := Model{state: stateExecutingTool}
	ctx := m.newTurnContext()

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() == nil {
		t.Fatal("esc during stateExecutingTool did not cancel the in-flight context")
	}
}

func TestEscDoesNothingWhenNothingIsRunning(t *testing.T) {
	m := Model{state: stateInput}
	// No newTurnContext call — cancelTurn is nil. Must not panic.
	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if got.(Model).state != stateInput {
		t.Error("state changed unexpectedly")
	}
}

func TestInterruptibleErrorText(t *testing.T) {
	if got := interruptibleErrorText(context.Canceled); got != "interrupted" {
		t.Errorf("got %q, want %q", got, "interrupted")
	}

	// errors.Is (not ==) is the point: a context.Canceled wrapped by a
	// real call chain (http.Client, BashSession, etc.) must still be
	// recognized, not just the bare sentinel value.
	wrapped := fmt.Errorf("request failed: %w", context.Canceled)
	if got := interruptibleErrorText(wrapped); got != "interrupted" {
		t.Errorf("got %q for a wrapped context.Canceled, want %q", got, "interrupted")
	}

	other := errors.New("connection refused")
	if got := interruptibleErrorText(other); got != "connection refused" {
		t.Errorf("got %q, want the original error text for a non-cancellation error", got)
	}
}

func TestInterruptibleResultText(t *testing.T) {
	if got := interruptibleResultText(context.Canceled.Error()); got != "interrupted" {
		t.Errorf("got %q, want %q", got, "interrupted")
	}
	if got := interruptibleResultText("file not found"); got != "file not found" {
		t.Errorf("got %q, want unchanged", got)
	}
}

// TestEscActuallyAbortsALiveRequest is the real end-to-end proof: a slow
// HTTP server, a real client.SendStreaming call, and esc genuinely
// unblocking it well before the server would ever respond — not just
// unit-level checks that a context's Done channel fires.
func TestEscActuallyAbortsALiveRequest(t *testing.T) {
	blockForever := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockForever
	}))
	defer server.Close()
	defer close(blockForever)

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	ctx := m.newTurnContext()

	cmd := startStream(ctx, m.client, []agent.Message{{Role: "user", Content: "hi"}})

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	// Give the request a moment to actually reach the (blocked) handler,
	// then interrupt exactly the way a keypress would.
	time.Sleep(50 * time.Millisecond)
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	select {
	case msg := <-done:
		ev, ok := msg.(streamEventMsg)
		if !ok {
			t.Fatalf("expected streamEventMsg, got %T", msg)
		}
		if ev.event.Err == nil {
			t.Fatal("expected an error after esc, got nil")
		}
		if !errors.Is(ev.event.Err, context.Canceled) {
			t.Errorf("event.Err = %v, want it to wrap context.Canceled", ev.event.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("esc did not unblock the in-flight request within 3s — the server would otherwise block forever")
	}
}
