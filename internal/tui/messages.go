package tui

import "github.com/BikeshR/chisel/internal/agent"

// streamEventMsg wraps one agent.Event as a tea.Msg, carrying the channel
// along so Update can re-arm listening for the next one.
type streamEventMsg struct {
	event agent.Event
	ch    <-chan agent.Event
}

// toolResultMsg carries the result of executing a single tool call.
// interrupted is set when the call's own context was cancelled (esc) —
// executeTool checks ctx.Err() directly rather than leaving handleToolResult
// to infer it from the stringified error content, which already broke
// for a wrapped MCP error (see interruptibleResultText's own history).
type toolResultMsg struct {
	result      agent.ToolResult
	interrupted bool
}

// modelCheckResultMsg carries the outcome of a /model check.
type modelCheckResultMsg struct {
	model string
	reply string
	usage agent.Usage
	err   error
}

// sessionSaveErrorMsg reports that persisting the session to disk failed.
// The conversation itself is unaffected — this is surfaced so silent data
// loss (a session that fails to resume next time) isn't fully silent.
type sessionSaveErrorMsg struct {
	err error
}

// historySaveErrorMsg reports that persisting one entry of prompt
// recall history (internal/history) failed — recall for the rest of
// this session is unaffected, only future-session persistence of this
// one entry is at risk, so this is worth a quiet note rather than
// anything louder.
type historySaveErrorMsg struct {
	err error
}

// autoCommitResultMsg carries the outcome of a /git auto commit attempt.
type autoCommitResultMsg struct {
	sha string
	err error
}

// compactResultMsg carries the outcome of a /compact request.
type compactResultMsg struct {
	summary string
	usage   agent.Usage
	err     error
}
