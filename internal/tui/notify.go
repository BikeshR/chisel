package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// notifyIdleMsg carries the message notifyIdle wants shown, for Update
// to turn into a queued escape sequence — see notifyIdle's own doc
// comment for why this doesn't just write to stdout directly.
type notifyIdleMsg struct {
	message string
}

// notifyIdle rings the terminal bell and, for terminals that support it
// (iTerm2 and derivatives; ignored harmlessly by ones that don't — an
// unrecognized OSC sequence is normally just swallowed), raises a desktop
// notification. Used whenever chisel stops needing to hold the model's
// or a tool's attention and instead needs the *user's* — a permission
// prompt appearing, or a turn finishing — so a long turn doesn't need
// babysitting.
//
// Deliberately doesn't write to os.Stdout directly from this Cmd's own
// goroutine: raw bytes written from anywhere other than bubbletea's own
// renderer goroutine risk interleaving with a frame it's mid-write on —
// the exact race the OSC-52 clipboard code (selection.go) documents
// avoiding for the same reason. Instead this fires a notifyIdleMsg;
// Update queues the escape sequence in Model.pendingNotifyOSC and View
// embeds it in the next rendered frame (zero-width, invisible), the
// same safe path clipboard-copy already uses.
func notifyIdle(message string) tea.Cmd {
	return func() tea.Msg { return notifyIdleMsg{message: message} }
}

// osc9Notify formats an OSC 9 desktop-notification escape sequence.
func osc9Notify(message string) string {
	return "\x1b]9;" + message + "\x07"
}

// clearNotifyOSCMsg clears the one-shot bell+OSC-9 sequence queued in
// Model.pendingNotifyOSC once it's had a render cycle to actually reach
// the terminal — the same one-shot-then-clear pattern
// clearClipboardOSCMsg already uses for the OSC-52 clipboard sequence.
type clearNotifyOSCMsg struct{}

// clearNotifyOSCDelay gives the renderer a moment to actually paint a
// frame carrying pendingNotifyOSC before it's cleared. Firing the clear
// immediately (an instant Cmd, dispatched right alongside the one that
// sets it) risked both landing within the same render tick — the set
// and the clear processed back to back with no frame painted in
// between, silently dropping the notification. A var, not a const, so
// a test can shrink it rather than actually sleeping.
var clearNotifyOSCDelay = 100 * time.Millisecond

func clearNotifyOSCCmd() tea.Cmd {
	return tea.Tick(clearNotifyOSCDelay, func(time.Time) tea.Msg { return clearNotifyOSCMsg{} })
}
