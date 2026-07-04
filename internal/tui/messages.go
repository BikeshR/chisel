package tui

import "github.com/BikeshR/chisel/internal/agent"

// streamEventMsg wraps one agent.Event as a tea.Msg, carrying the channel
// along so Update can re-arm listening for the next one.
type streamEventMsg struct {
	event agent.Event
	ch    <-chan agent.Event
}

// toolResultMsg carries the result of executing a single tool call.
type toolResultMsg struct {
	result agent.ToolResult
}

// modelCheckResultMsg carries the outcome of a /model check.
type modelCheckResultMsg struct {
	model string
	reply string
	err   error
}

// sessionSaveErrorMsg reports that persisting the session to disk failed.
// The conversation itself is unaffected — this is surfaced so silent data
// loss (a session that fails to resume next time) isn't fully silent.
type sessionSaveErrorMsg struct {
	err error
}
