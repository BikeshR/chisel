package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

// TestHandleStreamCompleteTriggersAutoCompact covers the "small, mostly
// wiring" feature from the mainstream-CLI-agent review: chisel already
// had /compact and a status-bar warning past contextWarnThreshold, but
// required the user to notice and type /compact themselves. This is
// the regression test for that gap — a turn ending with a large enough
// lastContextTokens should trigger compaction automatically rather than
// just going idle.
func TestHandleStreamCompleteTriggersAutoCompact(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, cmd := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{InputTokens: contextWarnThreshold + 1},
		"stop",
	)
	gotModel := got.(Model)

	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel (auto-compact should start a new turn)", gotModel.state)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the compaction request")
	}

	found := false
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "compacting automatically") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a notice that auto-compaction is starting", gotModel.renderedLines())
	}
}

// TestHandleStreamCompleteSuppressesIdleNotificationWhenAutoCompacting
// is the regression test for a real UX bug: the "chisel is done" bell/
// OSC-9 notification fired even when auto-compact was about to
// immediately start a new turn — the same "user's already mid-flow,
// don't notify" reasoning already applied to a queued message just
// wasn't applied to this other "chisel is about to be busy again
// immediately" case.
func TestHandleStreamCompleteSuppressesIdleNotificationWhenAutoCompacting(t *testing.T) {
	// A real server so invoking every batched sub-Cmd (including
	// compact's own, which makes a real client.SendStreaming call) is
	// safe and fast rather than hitting an unreachable default URL.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	m := Model{client: agent.New("minimax-m3")}
	_, cmd := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{InputTokens: contextWarnThreshold + 1},
		"stop",
	)
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	for _, sub := range unpackBatch(t, cmd) {
		if sub == nil {
			continue
		}
		if _, ok := sub().(notifyIdleMsg); ok {
			t.Error("expected no notifyIdleMsg when auto-compact is about to start immediately")
		}
	}
}

func TestHandleStreamCompleteNoAutoCompactBelowThreshold(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, _ := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{InputTokens: contextWarnThreshold - 1},
		"stop",
	)
	gotModel := got.(Model)

	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput — should stay idle below the threshold", gotModel.state)
	}
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "compacting automatically") {
			t.Errorf("lines = %+v, want no auto-compact notice below the threshold", gotModel.renderedLines())
		}
	}
}

// TestHandleStreamCompleteSkipsAutoCompactWithQueuedMessage: compacting
// is itself a turn, and delivering an already-queued message means the
// user is already mid-flow — auto-compact shouldn't insert an extra
// step in between.
func TestHandleStreamCompleteSkipsAutoCompactWithQueuedMessage(t *testing.T) {
	m := Model{
		client:         agent.New("minimax-m3"),
		queuedMessages: []string{"next thing to do"},
	}
	got, _ := m.handleStreamComplete(
		agent.Message{Role: "assistant", Content: "done"},
		agent.Usage{InputTokens: contextWarnThreshold + 1},
		"stop",
	)
	gotModel := got.(Model)

	// dequeueOrSubmit should have delivered the queued message instead
	// of auto-compacting — state ends up stateWaitingModel either way,
	// so check the actual message content, not just the state. messages[0]
	// is the assistant reply handleStreamComplete itself just appended;
	// messages[1] is the delivered queued message.
	if len(gotModel.messages) != 2 || gotModel.messages[1].Content != "next thing to do" {
		t.Errorf("messages = %+v, want the queued message submitted instead of auto-compacting", gotModel.messages)
	}
	for _, l := range gotModel.renderedLines() {
		if strings.Contains(l, "compacting automatically") {
			t.Errorf("lines = %+v, want no auto-compact notice when a message is queued", gotModel.renderedLines())
		}
	}
}
