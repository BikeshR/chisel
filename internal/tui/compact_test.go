package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/hooks"
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
	got, cmd := m.handleCompactCommand("")
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
	got, cmd := m.handleCompactCommand("")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the compaction request")
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", got.state)
	}
}

func TestHandleCompactCommandShowsFocusInNotice(t *testing.T) {
	m := Model{messages: []agent.Message{{Role: "user", Content: "hi"}}}
	got, _ := m.handleCompactCommand("the auth refactor")
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "the auth refactor") {
		t.Errorf("lines = %+v, want the focus text shown in the compacting notice", lines)
	}
}

// compactRequestBody decodes just the fields these tests need out of a
// real request body — internal/agent's own chatRequest is unexported,
// so a test outside that package can't reference it directly.
type compactRequestBody struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// TestCompactFoldsFocusIntoPrompt verifies the actual request body sent
// on the wire includes the focus instruction, not just that
// handleCompactCommand accepted the argument.
func TestCompactFoldsFocusIntoPrompt(t *testing.T) {
	var gotBody compactRequestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "hi"}}, "the auth refactor", "", hooks.Config{})
	msg := cmd().(compactResultMsg)
	if msg.err != nil {
		t.Fatalf("compact: %v", msg.err)
	}

	last := gotBody.Messages[len(gotBody.Messages)-1]
	if !strings.Contains(last.Content, "the auth refactor") {
		t.Errorf("last message = %q, want the focus instruction included", last.Content)
	}
}

// TestCompactWithoutFocusDoesNotMentionIt confirms the empty-focus path
// (auto-compact, or a bare /compact) doesn't add an empty or malformed
// "Pay particular attention to:" fragment.
func TestCompactWithoutFocusDoesNotMentionIt(t *testing.T) {
	var gotBody compactRequestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "hi"}}, "", "", hooks.Config{})
	msg := cmd().(compactResultMsg)
	if msg.err != nil {
		t.Fatalf("compact: %v", msg.err)
	}

	last := gotBody.Messages[len(gotBody.Messages)-1]
	if strings.Contains(last.Content, "Pay particular attention to") {
		t.Errorf("last message = %q, want no focus fragment with an empty focus", last.Content)
	}
}

// TestCompactRunsPreCompactHookWithTranscript is the direct regression
// test for the feature: compact must run any configured PreCompact
// hooks before the actual compaction request, with the full
// conversation available to the hook via CHISEL_HOOK_TRANSCRIPT_PATH.
func TestCompactRunsPreCompactHookWithTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	dir := t.TempDir()
	backupPath := dir + "/backup.md"
	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreCompact = []hooks.Hook{{Command: `cp "$CHISEL_HOOK_TRANSCRIPT_PATH" "` + backupPath + `"`}}

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "the actual conversation content"}}, "", dir, hooksCfg)
	msg := cmd().(compactResultMsg)
	if msg.err != nil {
		t.Fatalf("compact: %v", msg.err)
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("expected the PreCompact hook to have run and backed up the transcript: %v", err)
	}
	if !strings.Contains(string(data), "the actual conversation content") {
		t.Errorf("backup content = %q, want the real conversation content present", data)
	}
}

// TestCompactWithoutPreCompactHooksSkipsTempFile confirms the no-hooks
// path doesn't even create a temp file — nothing to observe directly,
// but this at least exercises it alongside the real compaction request
// completing normally.
func TestCompactWithoutPreCompactHooksSkipsTempFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"summary\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "hi"}}, "", t.TempDir(), hooks.Config{})
	msg := cmd().(compactResultMsg)
	if msg.err != nil {
		t.Fatalf("compact: %v", msg.err)
	}
	if msg.summary != "summary" {
		t.Errorf("summary = %q", msg.summary)
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

// TestHandleCompactResultDeliversQueuedMessage is the regression test
// for a real bug: handleCompactResult returned to stateInput without
// ever calling dequeueOrSubmit, so a message typed while compacting was
// announced "→ queued: …" and then stranded — including after an
// *auto*-triggered /compact, reachable without the user ever typing
// /compact themselves (see the auto-compact branch in
// handleStreamComplete).
func TestHandleCompactResultDeliversQueuedMessage(t *testing.T) {
	m := Model{
		client:         agent.New("minimax-m3"),
		state:          stateWaitingModel,
		messages:       []agent.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}},
		queuedMessages: []string{"what's next"},
	}
	got, cmd := m.handleCompactResult(compactResultMsg{summary: "did X"})
	gotModel := got.(Model)

	if len(gotModel.queuedMessages) != 0 {
		t.Errorf("queuedMessages = %+v, want the queued message delivered, not left stranded", gotModel.queuedMessages)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to deliver the queued message")
	}
	// The queued text became the next user message, on top of the
	// compacted (single-summary) history.
	if len(gotModel.messages) != 2 || gotModel.messages[1].Content != "what's next" {
		t.Errorf("messages = %+v, want the queued message appended after the compacted summary", gotModel.messages)
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

// TestCompactRefusesAToolCallResponse is the regression test for a real
// data-loss bug: /compact used to trust whatever the model returned as
// the summary, so a tool-happy model responding with a tool call
// instead of plain text (a real failure mode — the compact prompt
// explicitly asks about "files created or modified") would replace the
// whole conversation with an empty summary. compact() now sends the
// request via client.WithoutTools() and separately checks the response
// content isn't empty/tool-call-shaped.
func TestCompactRefusesAToolCallResponse(t *testing.T) {
	toolCallSSE := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"glob\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(toolCallSSE))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "hi"}}, "", "", hooks.Config{})
	msg := cmd().(compactResultMsg)

	if msg.err == nil {
		t.Fatal("expected an error when the model responds with a tool call instead of a summary")
	}
	if msg.summary != "" {
		t.Errorf("summary = %q, want empty on this failure path", msg.summary)
	}
}

func TestCompactRefusesEmptyContentResponse(t *testing.T) {
	emptySSE := "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\ndata: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(emptySSE))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	client := agent.New("minimax-m3")
	cmd := compact(context.Background(), client, []agent.Message{{Role: "user", Content: "hi"}}, "", "", hooks.Config{})
	msg := cmd().(compactResultMsg)

	if msg.err == nil {
		t.Fatal("expected an error when the model responds with empty content")
	}
}
