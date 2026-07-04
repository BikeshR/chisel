package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
)

func TestFormatTokenCount(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		999:     "999",
		1000:    "1.0k",
		12400:   "12.4k",
		999999:  "1000.0k",
		1000000: "1.0M",
		2500000: "2.5M",
	}
	for n, want := range cases {
		if got := formatTokenCount(n); got != want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCompactedHistory(t *testing.T) {
	msgs := compactedHistory("did some stuff")
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "did some stuff") {
		t.Errorf("content = %q, want it to contain the summary", msgs[0].Content)
	}
}

func TestHandleCompactCommandEmptyHistory(t *testing.T) {
	m := Model{}
	got, cmd := m.handleCompactCommand()
	if cmd != nil {
		t.Error("expected a nil Cmd when there's nothing to compact")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing to compact") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleCompactCommandStartsAsync(t *testing.T) {
	m := Model{messages: []agent.Message{{Role: "user", Content: "hi"}}}
	got, cmd := m.handleCompactCommand()
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the compaction request")
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", got.state)
	}
}

func TestHandleCompactResultSuccess(t *testing.T) {
	m := Model{
		state:    stateWaitingModel,
		messages: []agent.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}},
		entries:  []entry{{styled: "you  a"}, {styled: "chisel  b"}},
	}
	got, cmd := m.handleCompactResult(compactResultMsg{
		summary: "we did X and Y",
		usage:   agent.Usage{InputTokens: 100, OutputTokens: 20},
	})
	gotModel := got.(Model)

	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput", gotModel.state)
	}
	if cmd == nil {
		t.Error("expected a save-session Cmd after a successful compact")
	}
	if len(gotModel.messages) != 1 {
		t.Fatalf("messages = %+v, want history replaced with a single summary message", gotModel.messages)
	}
	if !strings.Contains(gotModel.messages[0].Content, "we did X and Y") {
		t.Errorf("compacted message = %q", gotModel.messages[0].Content)
	}
	if gotModel.tokensIn != 100 || gotModel.tokensOut != 20 {
		t.Errorf("tokensIn/tokensOut = %d/%d, want 100/20 (compaction's own usage still counts)", gotModel.tokensIn, gotModel.tokensOut)
	}
	lines := gotModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "we did X and Y") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the summary to be shown", lines)
	}
}

func TestHandleCompactResultError(t *testing.T) {
	m := Model{
		state:    stateWaitingModel,
		messages: []agent.Message{{Role: "user", Content: "a"}},
	}
	got, cmd := m.handleCompactResult(compactResultMsg{err: errors.New("stream failed")})
	gotModel := got.(Model)

	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput", gotModel.state)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd on compact failure")
	}
	if len(gotModel.messages) != 1 {
		t.Errorf("messages = %+v, want the original history preserved on failure", gotModel.messages)
	}
	lines := gotModel.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "stream failed") {
		t.Errorf("lines = %+v, want an error line mentioning the failure", lines)
	}
}
