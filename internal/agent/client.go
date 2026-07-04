// Package agent wraps chisel's provider — OpenCode Go's OpenAI-compatible
// chat-completions API — into the tool-calling loop chisel runs: send the
// conversation, execute whatever tools come back, send the results, repeat
// until the model stops asking for tools. No SDK in between; chisel speaks
// the wire format directly.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const systemPrompt = `You are chisel, a terminal coding agent running in the user's project directory.

Use the available tools to read, search, and edit files, and to run shell commands. Prefer glob and grep for finding things over reading whole directories blind. Make the smallest change that correctly does what was asked — don't refactor or add abstractions beyond what the task requires.

When you're done, say so plainly and stop; don't ask what to do next unless the request was genuinely ambiguous.`

// planModeNote is appended to the system prompt while plan mode is on.
// It's an instruction, not the actual guarantee — any mutating tool call
// the model makes anyway is hard-denied at dispatch time regardless of
// what it says here (see internal/tui/model.go's dispatchNextTool), so a
// model that ignores this can't actually do anything, it just wastes a
// turn finding out.
const planModeNote = `

You are currently in PLAN MODE. Only use read-only exploration — viewing files, glob, grep, and inspection-only shell commands (ls, cat, grep, etc.) — to understand what's being asked. Do not attempt any file edit or state-changing command; those will be refused. Once you understand what's needed, present a clear, concise, numbered plan of the specific changes you'd make, then stop and wait — don't start making changes. The user will exit plan mode when they want you to proceed.`

const defaultBaseURL = "https://opencode.ai/zen/go"

// Client sends conversation turns to a single OpenCode Go model with
// chisel's fixed tool set.
type Client struct {
	http     *http.Client
	baseURL  string
	apiKey   string
	model    string
	tools    []Tool
	planMode bool
	memory   string
}

// New builds a Client for the given model. Configured via CHISEL_API_KEY
// (required) and optionally CHISEL_BASE_URL (defaults to OpenCode Go's
// endpoint).
func New(model string) *Client {
	baseURL := os.Getenv("CHISEL_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		http:    &http.Client{Transport: newTransport()},
		baseURL: baseURL,
		apiKey:  os.Getenv("CHISEL_API_KEY"),
		model:   model,
		tools:   buildTools(),
	}
}

// newTransport bounds how long a request can wait for a response to
// *start* — a genuinely stuck connection that never replies at all —
// without bounding how long a streaming response body can keep sending
// chunks afterward. http.Client.Timeout can't express that distinction:
// its docs are explicit that it "includes ... reading the response
// body", so a flat 5-minute Client.Timeout (the previous approach here)
// would abort a long but actively-streaming turn — a big multi-tool-call
// response, or just a verbose model — well before anything was actually
// wrong.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

// ModelName returns the model this client sends every request to.
func (c *Client) ModelName() string {
	return c.model
}

// SetModel switches which model this client sends requests to, in place
// — unlike constructing a fresh Client via New, this preserves whatever
// was already configured on it: MCP tools added via AddTools, plan mode,
// and memory content. Switching the model is not the same thing as
// starting over.
func (c *Client) SetModel(model string) {
	c.model = model
}

// AddTools appends to the tool set sent with every request — for tools
// discovered at runtime (MCP servers) rather than chisel's fixed built-in
// set from buildTools.
func (c *Client) AddTools(tools []Tool) {
	c.tools = append(c.tools, tools...)
}

// SetTools replaces the tool set sent with future requests outright —
// unlike AddTools, which appends to whatever New already set up via
// buildTools. Used by headless mode (chisel -p) to restrict to
// ReadOnlyTools: a non-interactive invocation has no terminal to show a
// permission prompt to, so nothing offered can need one in the first place.
func (c *Client) SetTools(tools []Tool) {
	c.tools = tools
}

// RemoveToolsWithPrefix drops every tool whose name starts with prefix
// from the set sent with future requests — used once an MCP server is
// discovered to be broken (see mcp.Server, whose tools are all named
// mcp__<server>__<tool>): there's no point continuing to offer the
// model a tool that will just fail every time, with no way back short
// of restarting chisel entirely.
func (c *Client) RemoveToolsWithPrefix(prefix string) {
	kept := c.tools[:0]
	for _, t := range c.tools {
		if !strings.HasPrefix(t.Function.Name, prefix) {
			kept = append(kept, t)
		}
	}
	c.tools = kept
}

// SetPlanMode toggles plan mode, which appends planModeNote to the system
// prompt sent with every request from here on.
func (c *Client) SetPlanMode(enabled bool) {
	c.planMode = enabled
}

// PlanMode reports whether plan mode is currently on.
func (c *Client) PlanMode() bool {
	return c.planMode
}

// Clone returns a copy of c for model — same tools (including any added
// via AddTools) and memory, but not plan mode, since a clone's only
// current use (checkModel, for /model check) is a one-off "does this
// model even work" probe, not a real turn where plan mode would matter.
// Exists so /model check tests a candidate model through chisel's real
// request shape instead of a bare client with none of that context —
// the same class of failure (a provider rejecting the tool set
// outright) can differ once MCP servers are actually configured.
func (c *Client) Clone(model string) *Client {
	clone := *c
	clone.model = model
	clone.planMode = false
	return &clone
}

// WithoutTools returns a copy of c with no tools declared — used by
// /compact, whose one request just wants a plain-text summary and has
// no legitimate reason to call any tool. A model can, in principle,
// still return a tool call despite none being declared, so this narrows
// the failure mode rather than eliminating it outright — callers should
// still guard against an empty or tool-call response (see compact in
// tui/model.go).
func (c *Client) WithoutTools() *Client {
	clone := *c
	clone.tools = nil
	return &clone
}

// SetMemory sets the CHISEL.md content (see internal/memory) appended to
// the system prompt sent with every request from here on. Pass "" to
// clear it.
func (c *Client) SetMemory(text string) {
	c.memory = text
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

// SendStreaming starts one chat-completion request and returns a channel of
// Events: incremental text deltas, then a final event carrying either the
// complete accumulated Message or an error. The HTTP request itself
// (status code, connection) is validated before this returns — only
// decode-time failures arrive over the channel.
func (c *Client) SendStreaming(ctx context.Context, history []Message) (<-chan Event, error) {
	prompt := systemPrompt
	if c.planMode {
		prompt += planModeNote
	}
	if c.memory != "" {
		prompt += "\n\n---\n\nProject and user instructions:\n\n" + c.memory
	}
	messages := append([]Message{{Role: "system", Content: prompt}}, history...)

	body, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    c.tools,
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.apiKey)
	req.Header.Set("accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("%s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, describeError(data))
	}

	ch := make(chan Event)
	go decodeStream(resp.Body, ch)
	return ch, nil
}

func describeError(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return strings.TrimSpace(string(body))
}

// streamChunk mirrors one SSE "data:" line's JSON payload for the fields
// chisel actually uses. Usage arrives on its own chunk, with an empty
// choices array, once the response is complete.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// decodeStream reads Server-Sent Events from r, accumulating them into a
// single Message and emitting a TextDelta Event per content chunk along
// the way. The provider appends a few of its own non-standard trailing
// frames after "[DONE]" (cost/usage bookkeeping) — decoding stops at
// "[DONE]" and ignores anything after, per the OpenAI streaming
// convention this otherwise follows.
// sseReadIdleTimeout bounds how long decodeStream will wait for the
// *next* line of an in-progress response before treating the
// connection as stalled. Neither ResponseHeaderTimeout (which only
// bounds getting the initial headers) nor esc-to-interrupt (which only
// helps if someone happens to be watching and notices nothing is
// happening) covers a body that stops sending data without ever
// actually closing — this is the backstop for that. A var, not a
// const, so tests can shorten it.
var sseReadIdleTimeout = 60 * time.Second

func decodeStream(body io.ReadCloser, ch chan<- Event) {
	defer close(ch)
	defer func() { _ = body.Close() }()

	var content strings.Builder
	var finishReason string
	var usage Usage
	toolCalls := map[int]*ToolCall{}
	var toolCallOrder []int

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Scanning runs in its own goroutine so the select loop below can
	// race it against an idle timer — scanner.Scan() has no per-read
	// deadline of its own. stop lets that goroutine exit on every path
	// out of this function (deferred, so it always closes) rather than
	// blocking forever trying to send a line nothing will ever receive
	// again — bufio.Scanner can have several lines already buffered
	// internally (OpenCode sends a couple of trailing bookkeeping frames
	// after "[DONE]", for instance), so a fixed-size buffered channel
	// isn't enough to guarantee that on its own. scanner.Err() is read
	// only inside this goroutine, right after Scan() returns false —
	// never from the loop below concurrently, which is what a bufio.Scanner
	// shared across two goroutines requires (its internal error field
	// isn't safe for that otherwise).
	type scanResult struct {
		line string
		more bool
		err  error
	}
	lines := make(chan scanResult)
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanResult{line: scanner.Text(), more: true}:
			case <-stop:
				return
			}
		}
		select {
		case lines <- scanResult{more: false, err: scanner.Err()}:
		case <-stop:
		}
	}()

	timer := time.NewTimer(sseReadIdleTimeout)
	defer timer.Stop()

readLoop:
	for {
		select {
		case r := <-lines:
			if !timer.Stop() {
				<-timer.C
			}
			if !r.more {
				if r.err != nil {
					ch <- Event{Done: true, Err: fmt.Errorf("read stream: %w", r.err)}
					return
				}
				break readLoop
			}
			timer.Reset(sseReadIdleTimeout)

			data, ok := strings.CutPrefix(r.line, "data:")
			if !ok {
				continue
			}
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				break readLoop
			}

			var chunk streamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				ch <- Event{Done: true, Err: fmt.Errorf("decode stream chunk: %w", err)}
				return
			}
			if chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.PromptTokens
				usage.OutputTokens = chunk.Usage.CompletionTokens
			}
			if len(chunk.Choices) == 0 {
				continue // vendor bookkeeping frame (e.g. cost tracking) or the usage-only chunk — nothing more to accumulate
			}

			choice := chunk.Choices[0]
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				ch <- Event{TextDelta: choice.Delta.Content}
			}
			for _, tc := range choice.Delta.ToolCalls {
				existing, seen := toolCalls[tc.Index]
				if !seen {
					existing = &ToolCall{Type: "function"}
					toolCalls[tc.Index] = existing
					toolCallOrder = append(toolCallOrder, tc.Index)
				}
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Function.Name != "" {
					existing.Function.Name = tc.Function.Name
				}
				existing.Function.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}

		case <-timer.C:
			ch <- Event{Done: true, Err: fmt.Errorf("stream stalled: no data received for %s", sseReadIdleTimeout)}
			return
		}
	}

	msg := Message{Role: "assistant", Content: content.String()}
	for _, idx := range toolCallOrder {
		msg.ToolCalls = append(msg.ToolCalls, *toolCalls[idx])
	}

	// A well-formed response always has content, a tool call, or a finish
	// reason. Zero of all three means something went wrong upstream in a
	// way that didn't surface as an HTTP error or a decode error.
	if msg.Content == "" && len(msg.ToolCalls) == 0 && finishReason == "" {
		ch <- Event{Done: true, Err: fmt.Errorf("no response from model (empty stream)")}
		return
	}

	ch <- Event{Done: true, Message: &msg, FinishReason: finishReason, Usage: usage}
}

// Drain reads ch to completion and returns the final response. Every
// caller that only cares about the end result (not the streamed text
// deltas along the way — chisel's own conversation loop in internal/tui
// does care and drains the channel itself instead) should go through
// this rather than hand-rolling "loop until Done, then check Err" and
// dereferencing Message directly: decodeStream's contract is that a Done
// event with Err == nil always carries a non-nil Message, but a contract
// enforced only by convention at the producer is exactly the kind of
// thing a future change can quietly break, and a bare `*final.Message`
// at every call site would then panic instead of erroring cleanly. This
// checks it once, explicitly.
func Drain(ch <-chan Event) (*Message, Usage, error) {
	var final Event
	var gotDone bool
	for ev := range ch {
		if ev.Done {
			final = ev
			gotDone = true
		}
	}
	if !gotDone {
		return nil, Usage{}, fmt.Errorf("stream closed without a final response")
	}
	if final.Err != nil {
		return nil, Usage{}, final.Err
	}
	if final.Message == nil {
		return nil, Usage{}, fmt.Errorf("no response from model")
	}
	return final.Message, final.Usage, nil
}
