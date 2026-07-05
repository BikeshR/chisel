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
	"sort"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/skill"
	"github.com/BikeshR/chisel/internal/subagentdef"
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

// Mode is the client's current operating mode — normal (every mutating
// call needs confirmation), accept-edits (file edits run without
// asking; bash and MCP calls still always ask), or plan (nothing
// mutating runs at all, hard-enforced at dispatch — see
// internal/tui/permission.go's decidePermission, the single place all
// three modes are actually acted on). A three-way enum rather than
// plan mode's original plain bool specifically so accept-edits could be
// added without a second, independent flag that would need its own
// interaction rules against plan mode (can't both be on at once).
type Mode int

const (
	ModeNormal Mode = iota
	ModeAcceptEdits
	ModePlan
)

// Client sends conversation turns to a single OpenCode Go model with
// chisel's fixed tool set.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
	tools   []Tool
	mode    Mode
	memory  string
	// plannerModel, if set, replaces model for requests sent while in
	// ModePlan — mirrors Goose's GOOSE_PLANNER_MODEL and Aider's
	// architect/editor split: a cheaper/faster model can handle
	// exploration-and-propose turns while the primary model does the
	// real work, using two model IDs from the one provider chisel
	// already talks to, not a second provider abstraction. Empty means
	// no split configured — plan mode just uses model, as it always did.
	plannerModel string
	// agentMemory is the project's .chisel/MEMORY.md content (see
	// internal/agentmemory) — notes the model wrote to itself via the
	// remember tool in a past session, kept in its own system-prompt
	// section distinct from memory (user-authored CHISEL.md/AGENTS.md)
	// so it's clear which is which.
	agentMemory string
	// skillsPrompt is the pre-formatted "available skills" section built
	// by SetSkills — just names and descriptions, never full skill
	// content, which stays out of every request until load_skill is
	// actually called for one.
	skillsPrompt string
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

// SetPlannerModel sets (or, with "", clears) the model used instead of
// ModelName while in ModePlan — see plannerModel's own doc comment.
func (c *Client) SetPlannerModel(model string) {
	c.plannerModel = model
}

// PlannerModel returns the configured planner model, or "" if none is set.
func (c *Client) PlannerModel() string {
	return c.plannerModel
}

// EffectiveModelName returns whichever model a request sent right now
// would actually use — plannerModel while in ModePlan (if one is
// configured), ModelName otherwise. Callers that care what's actually
// about to run (the status bar, dispatch_subagent picking a model for
// its child) should use this; callers that specifically mean "the
// primary model" regardless of mode (the /model picker's own
// current-selection marker, /model itself switching it) should keep
// using ModelName.
func (c *Client) EffectiveModelName() string {
	if c.mode == ModePlan && c.plannerModel != "" {
		return c.plannerModel
	}
	return c.model
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
//
// Allocates a fresh slice rather than compacting into c.tools[:0] —
// Clone/WithoutTools do a shallow struct copy, so a clone's own tools
// field shares this same backing array at the moment it's made.
// Compacting in place would silently overwrite whatever elements a
// clone's slice still (logically) contains within its own length,
// even though nothing ever assigns to clone.tools directly. Nothing
// races on this concurrently today — the state machine sequences a new
// turn (the only caller of syncMCPHealth, which calls this) after
// whatever probe client /model check cloned has already finished using
// its own tools — but it's exactly the kind of latent aliasing a later
// change (a background MCP health check, say) would trip over first
// and hardest to diagnose.
func (c *Client) RemoveToolsWithPrefix(prefix string) {
	kept := make([]Tool, 0, len(c.tools))
	for _, t := range c.tools {
		if !strings.HasPrefix(t.Function.Name, prefix) {
			kept = append(kept, t)
		}
	}
	c.tools = kept
}

// SetMode changes the client's operating mode outright — see Mode's own
// doc comment for what each value means.
func (c *Client) SetMode(m Mode) {
	c.mode = m
}

// Mode reports the client's current operating mode.
func (c *Client) Mode() Mode {
	return c.mode
}

// SetPlanMode and PlanMode predate the three-way Mode enum and are kept
// as convenience wrappers for the many callers (and tests) that only
// ever cared about the plan/not-plan distinction specifically, not
// accept-edits. SetPlanMode(false) only steps back to ModeNormal if
// plan mode was actually what was on — it must not clobber
// ModeAcceptEdits if that's somehow what's active when it's called
// (today nothing does that, but "false" meaning "whatever the mode
// was, un-plan it" is the only sane reading once a third mode exists).
func (c *Client) SetPlanMode(enabled bool) {
	if enabled {
		c.mode = ModePlan
	} else if c.mode == ModePlan {
		c.mode = ModeNormal
	}
}

// PlanMode reports whether plan mode specifically is currently on.
func (c *Client) PlanMode() bool {
	return c.mode == ModePlan
}

// Clone returns a copy of c for model — same tools (including any added
// via AddTools) and memory, but always ModeNormal regardless of what
// mode c is actually in, since a clone's only current use (checkModel,
// for /model check) is a one-off "does this model even work" probe, not
// a real turn where the mode would matter. Exists so /model check tests
// a candidate model through chisel's real request shape instead of a
// bare client with none of that context — the same class of failure (a
// provider rejecting the tool set outright) can differ once MCP servers
// are actually configured.
func (c *Client) Clone(model string) *Client {
	clone := *c
	clone.model = model
	clone.mode = ModeNormal
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

// SetMemory sets the CHISEL.md/AGENTS.md content (see internal/memory)
// appended to the system prompt sent with every request from here on.
// Pass "" to clear it.
func (c *Client) SetMemory(text string) {
	c.memory = text
}

// SetAgentMemory sets the project's .chisel/MEMORY.md content (see
// internal/agentmemory) — notes the model persisted to itself in a past
// session via the remember tool, appended to the system prompt in its
// own section, separate from memory. Pass "" to clear it. Unlike
// memory, this is also the tool's own load-bearing state: /memory clear
// calls this with "" after deleting the file, so a live session doesn't
// keep sending stale content the model itself asked to forget.
func (c *Client) SetAgentMemory(text string) {
	c.agentMemory = text
}

// SetSkills tells the model what skills (see internal/skill) are
// available — just their names and descriptions, appended to the
// system prompt, not their full content: that stays out of every
// request until the model actually calls load_skill for one. A no-op
// for an empty map. Also adds load_skill to the tool set, so it's only
// ever offered when there's at least one skill to load.
func (c *Client) SetSkills(skills map[string]skill.Skill) {
	if len(skills) == 0 {
		return
	}

	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Available skills — call load_skill with a name below to pull in its full instructions when it's relevant to what you're doing:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %s\n", name, skills[name].Description)
	}
	c.skillsPrompt = strings.TrimRight(b.String(), "\n")

	c.AddTools([]Tool{loadSkillTool()})
}

// SetSubagents tells the model what custom subagent roles (see
// internal/subagentdef) are available — folded into dispatch_subagent's
// own tool schema (an "agent" enum plus each role's description), not a
// separate tool or system-prompt section, since dispatch_subagent
// itself is the only thing a role selection actually affects. A no-op
// for an empty map — dispatch_subagent keeps its original,
// single-role schema. Doesn't change what any subagent can actually
// do: every custom role still runs with exactly subagentTools() (see
// runDispatchSubagent), a definition only supplies a prompt layered on
// top of the task, never a different tool set.
func (c *Client) SetSubagents(subagents map[string]subagentdef.Subagent) {
	if len(subagents) == 0 {
		return
	}
	for i, t := range c.tools {
		if t.Function.Name == "dispatch_subagent" {
			c.tools[i] = subagentDispatchTool(subagents)
			return
		}
	}
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
	messages := append([]Message{{Role: "system", Content: c.systemPromptSections().full()}}, history...)

	body, err := json.Marshal(chatRequest{
		Model:    c.EffectiveModelName(),
		Messages: messages,
		Tools:    c.tools,
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	resp, err := c.doWithRetry(ctx, body)
	if err != nil {
		return nil, err
	}

	ch := make(chan Event)
	go decodeStream(resp.Body, ch)
	return ch, nil
}

// maxSendAttempts bounds how many times doWithRetry will try a request
// that failed for a plausibly transient reason — a dropped connection,
// or the provider returning 429/500/502/503/504 — before giving up.
// Anything else (a 400, a 401, any other 4xx) is returned immediately:
// retrying a request the client got wrong would just repeat the same
// failure, not recover from it.
const maxSendAttempts = 3

// retryBackoffFunc returns how long to wait before retrying, given how
// many attempts have already failed (0 for the first retry). Exponential
// starting at 1s, capped at 10s so maxSendAttempts' worth of retries
// finishes in well under a minute even in the worst case. A var (not a
// plain function), the same reasoning as sseReadIdleTimeout: tests
// shrink it so retry tests don't spend real seconds sleeping.
var retryBackoffFunc = func(failedAttempts int) time.Duration {
	d := time.Second << failedAttempts
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

// isRetryableStatus reports whether an HTTP status code is worth
// retrying — a rate limit or a server-side hiccup, not a request the
// client itself got wrong.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// doWithRetry sends the request built from body, retrying transient
// failures up to maxSendAttempts times with backoff between attempts.
// body is re-read fresh (via bytes.NewReader) on every attempt — a
// *http.Request's body can't be replayed once consumed, so this builds
// a new request each time rather than reusing one across retries. On
// success, the caller owns the returned response's body and must close
// it (same contract SendStreaming always had); on failure, every
// attempt's body is drained and closed here before returning.
func (c *Client) doWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryBackoffFunc(attempt - 1)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
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
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		statusErr := fmt.Errorf("%s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, describeError(data))
		if !isRetryableStatus(resp.StatusCode) {
			return nil, statusErr
		}
		lastErr = statusErr
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", maxSendAttempts, lastErr)
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
