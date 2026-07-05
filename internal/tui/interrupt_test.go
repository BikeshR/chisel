package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/hooks"
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

// TestHandleToolResultInterruptedStopsWholeTurn is the regression test
// for a real bug: esc during a tool call only ever cancelled that one
// call's own context — newTurnContext hands the *next* pending call (if
// any) a fresh, uncancelled context, so the turn just continued: the
// rest of pendingUses dispatched normally, and once they all resolved
// chisel went right back to the model with them, which typically just
// retried. A single esc must stop the whole turn, not one call in it.
func TestHandleToolResultInterruptedStopsWholeTurn(t *testing.T) {
	m := Model{
		workDir: t.TempDir(),
		state:   stateExecutingTool,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash"}},
			{ID: "call_2", Function: agent.ToolCallFunction{Name: "bash"}},
			{ID: "call_3", Function: agent.ToolCallFunction{Name: "bash"}},
		},
	}

	got, cmd := m.handleToolResult(agent.ToolResult{ID: "call_1", Content: context.Canceled.Error(), IsError: true}, true)
	gotModel := got.(Model)

	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput — esc during a tool call must stop the whole turn", gotModel.state)
	}
	if len(gotModel.pendingUses) != 0 {
		t.Errorf("pendingUses = %+v, want all resolved rather than left to dispatch normally", gotModel.pendingUses)
	}
	if len(gotModel.messages) != 3 {
		t.Fatalf("messages = %+v, want 3 tool results — one real (call_1), two synthetic (call_2, call_3)", gotModel.messages)
	}
	for i, id := range []string{"call_1", "call_2", "call_3"} {
		if gotModel.messages[i].ToolCallID != id {
			t.Errorf("messages[%d].ToolCallID = %q, want %q", i, gotModel.messages[i].ToolCallID, id)
		}
	}
	if !strings.Contains(gotModel.messages[1].Content, "Interrupted") {
		t.Errorf("messages[1] (never-run call_2) = %+v, want it marked as interrupted, not silently omitted", gotModel.messages[1])
	}

	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (save + flush + dequeue)")
	}
	found := false
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "2 more queued tool call") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line noting the 2 skipped calls", gotModel.renderedLines())
	}
}

// TestHandleToolResultNotInterruptedStillDispatchesRemainingCalls is the
// contrast case: a normal (non-cancelled) tool result with more pending
// calls must keep dispatching them exactly as before — the interrupted
// flag, not just a non-empty pendingUses, is what changed here.
func TestHandleToolResultNotInterruptedStillDispatchesRemainingCalls(t *testing.T) {
	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: t.TempDir(),
		state:   stateExecutingTool,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"echo hi"}`}},
			{ID: "call_2", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"echo bye"}`}},
		},
	}

	got, _ := m.handleToolResult(agent.ToolResult{ID: "call_1", Content: "hi"}, false)
	gotModel := got.(Model)

	if gotModel.state != stateAwaitingPermission && gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want the next pending call to still be dispatched (permission prompt or straight to execution)", gotModel.state)
	}
	if len(gotModel.pendingUses) != 1 || gotModel.pendingUses[0].ID != "call_2" {
		t.Errorf("pendingUses = %+v, want call_2 still queued to dispatch normally", gotModel.pendingUses)
	}
}

// TestExecuteToolMarksInterruptedOnCancelledContext confirms the signal
// handleToolResult's interrupted handling depends on: executeTool must
// set toolResultMsg.interrupted from ctx.Err() directly, not leave it to
// be inferred later from the stringified error content (which already
// broke once for a wrapped MCP error).
func TestExecuteToolMarksInterruptedOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call ever starts

	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"echo hi"}`}}
	bash := agent.NewBashSession(t.TempDir())
	defer bash.Close()

	cmd := executeTool(ctx, t.TempDir(), "minimax-m3", bash, nil, hooks.Config{}, nil, nil, "", call)
	msg := cmd()

	result, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg, got %T", msg)
	}
	if !result.interrupted {
		t.Error("expected interrupted = true for a call run with an already-cancelled context")
	}
	if !result.result.IsError {
		t.Error("expected the result itself to also be an error")
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
