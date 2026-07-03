package agent

// Message is one conversation turn, shaped for OpenAI's chat-completions
// API — the wire format chisel now speaks directly (OpenCode Go's
// /v1/chat/completions endpoint), with no SDK in between.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant messages only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool messages only
}

// Tool is a function definition offered to the model.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Event is one step of an in-flight response, delivered over the channel
// SendStreaming returns. Done is set exactly once, as the final event —
// either Message (success) or Err (failure) is populated, never both.
// FinishReason ("stop", "tool_calls", "length", ...) and Usage are
// metadata about the turn, not part of the message itself, so they travel
// alongside Message rather than inside it — Message stays the same shape
// whether it's fresh off the wire or being round-tripped back into a
// later request.
type Event struct {
	TextDelta    string
	Done         bool
	Message      *Message
	FinishReason string
	Usage        Usage
	Err          error
}

// Usage is token accounting for one request.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}
