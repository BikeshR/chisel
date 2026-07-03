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

const defaultBaseURL = "https://opencode.ai/zen/go"

// Client sends conversation turns to a single OpenCode Go model with
// chisel's fixed tool set.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
	tools   []Tool
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
		http:    &http.Client{Timeout: 5 * time.Minute},
		baseURL: baseURL,
		apiKey:  os.Getenv("CHISEL_API_KEY"),
		model:   model,
		tools:   buildTools(),
	}
}

// ModelName returns the model this client sends every request to.
func (c *Client) ModelName() string {
	return c.model
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
	messages := append([]Message{{Role: "system", Content: systemPrompt}}, history...)

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
		defer resp.Body.Close()
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
func decodeStream(body io.ReadCloser, ch chan<- Event) {
	defer close(ch)
	defer body.Close()

	var content strings.Builder
	var finishReason string
	var usage Usage
	toolCalls := map[int]*ToolCall{}
	var toolCallOrder []int

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
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
	}

	if err := scanner.Err(); err != nil {
		ch <- Event{Done: true, Err: fmt.Errorf("read stream: %w", err)}
		return
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
