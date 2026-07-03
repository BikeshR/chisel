package agent

import (
	"io"
	"strings"
	"testing"
)

// These fixtures are the actual SSE bytes captured from OpenCode Go
// (https://opencode.ai/zen/go, model minimax-m3) during development —
// not synthetic examples. Two real quirks they preserve: the model's own
// reasoning arrives inline in delta.content as <think>...</think>, not a
// separate field, and OpenCode appends its own non-standard bookkeeping
// frames after the terminating "data: [DONE]" line.

const plainTextFixture = `data: {"id":"6a1e8f69bc7fc17398f2529ddac29dfc","object":"chat.completion.chunk","created":1783120139,"model":"minimax-m3","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>\nThe user is"}}],"usage":null}

data: {"id":"6a1e8f69bc7fc17398f2529ddac29dfc","object":"chat.completion.chunk","created":1783120139,"model":"minimax-m3","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":" just asking me to say hi. Simple greeting.\n</think>\nHi there! 👋 How can I help you today?"}}],"usage":null}

data: {"id":"6a1e8f69bc7fc17398f2529ddac29dfc","object":"chat.completion.chunk","created":1783120139,"model":"minimax-m3","choices":[],"usage":{"prompt_tokens":178,"completion_tokens":27,"total_tokens":205}}

data: {"choices":[],"x-opencode-type":"inference-cost","cost":"0.00001948"}

data: [DONE]

data: {"choices":[],"cost":"0"}
`

const toolCallFixture = `data: {"id":"62a2410e8840b486c3fd6a521629bebb","object":"chat.completion.chunk","created":1783120158,"model":"minimax-m3","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>\nThe user wants me to use"}}],"usage":null}

data: {"id":"62a2410e8840b486c3fd6a521629bebb","object":"chat.completion.chunk","created":1783120158,"model":"minimax-m3","choices":[{"index":0,"delta":{"role":"assistant","content":" the glob tool to find all .go files.\n</think>\n"}}],"usage":null}

data: {"id":"62a2410e8840b486c3fd6a521629bebb","object":"chat.completion.chunk","created":1783120158,"model":"minimax-m3","choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_function_5nkrh3iajtsk_1","type":"function","function":{"name":"glob","arguments":"{\"pattern\": \"**/*.go\"}"}}]}}],"usage":null}

data: {"id":"62a2410e8840b486c3fd6a521629bebb","object":"chat.completion.chunk","created":1783120158,"model":"minimax-m3","choices":[],"usage":{"prompt_tokens":419,"completion_tokens":55,"total_tokens":474}}

data: {"choices":[],"x-opencode-type":"inference-cost","cost":"0.00003150"}

data: [DONE]

data: {"choices":[],"cost":"0"}
`

func runDecodeStream(t *testing.T, fixture string) []Event {
	t.Helper()
	ch := make(chan Event)
	go decodeStream(io.NopCloser(strings.NewReader(fixture)), ch)

	var events []Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestDecodeStreamPlainText(t *testing.T) {
	events := runDecodeStream(t, plainTextFixture)

	var deltas []string
	var final *Event
	for i, ev := range events {
		if ev.Done {
			e := events[i]
			final = &e
			continue
		}
		deltas = append(deltas, ev.TextDelta)
	}

	wantDeltas := []string{"<think>\nThe user is", " just asking me to say hi. Simple greeting.\n</think>\nHi there! 👋 How can I help you today?"}
	if len(deltas) != len(wantDeltas) {
		t.Fatalf("got %d text deltas, want %d: %#v", len(deltas), len(wantDeltas), deltas)
	}
	for i, d := range deltas {
		if d != wantDeltas[i] {
			t.Errorf("delta %d = %q, want %q", i, d, wantDeltas[i])
		}
	}

	if final == nil {
		t.Fatal("no final (Done) event received")
	}
	if final.Err != nil {
		t.Fatalf("unexpected error: %v", final.Err)
	}
	wantContent := "<think>\nThe user is just asking me to say hi. Simple greeting.\n</think>\nHi there! 👋 How can I help you today?"
	if final.Message.Content != wantContent {
		t.Errorf("accumulated content =\n  %q\nwant\n  %q", final.Message.Content, wantContent)
	}
	if len(final.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %+v", final.Message.ToolCalls)
	}
	if final.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q", final.FinishReason, "stop")
	}
	if final.Usage != (Usage{InputTokens: 178, OutputTokens: 27}) {
		t.Errorf("usage = %+v, want {178 27}", final.Usage)
	}
}

func TestDecodeStreamToolCall(t *testing.T) {
	events := runDecodeStream(t, toolCallFixture)

	var final *Event
	for i, ev := range events {
		if ev.Done {
			e := events[i]
			final = &e
		}
	}
	if final == nil {
		t.Fatal("no final (Done) event received")
	}
	if final.Err != nil {
		t.Fatalf("unexpected error: %v", final.Err)
	}

	if final.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want %q", final.FinishReason, "tool_calls")
	}
	if len(final.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(final.Message.ToolCalls), final.Message.ToolCalls)
	}
	tc := final.Message.ToolCalls[0]
	if tc.ID != "call_function_5nkrh3iajtsk_1" {
		t.Errorf("tool call ID = %q", tc.ID)
	}
	if tc.Function.Name != "glob" {
		t.Errorf("tool call name = %q, want %q", tc.Function.Name, "glob")
	}
	if tc.Function.Arguments != `{"pattern": "**/*.go"}` {
		t.Errorf("tool call arguments = %q", tc.Function.Arguments)
	}
	if final.Usage != (Usage{InputTokens: 419, OutputTokens: 55}) {
		t.Errorf("usage = %+v, want {419 55}", final.Usage)
	}
}

func TestDecodeStreamEmptyResponseIsAnError(t *testing.T) {
	// A response with zero content, zero tool calls, and no finish reason
	// shouldn't be mistaken for a legitimate empty success — this is the
	// exact silent-failure shape found (against a different endpoint)
	// earlier in this project's development.
	events := runDecodeStream(t, "data: [DONE]\n")

	if len(events) != 1 || !events[0].Done || events[0].Err == nil {
		t.Fatalf("expected exactly one Done event with a non-nil error, got %+v", events)
	}
}
