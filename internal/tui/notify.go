package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// notifyIdle rings the terminal bell and, for terminals that support it
// (iTerm2 and derivatives; ignored harmlessly by ones that don't — an
// unrecognized OSC sequence is normally just swallowed), raises a desktop
// notification. Used whenever chisel stops needing to hold the model's
// or a tool's attention and instead needs the *user's* — a permission
// prompt appearing, or a turn finishing — so a long turn doesn't need
// babysitting.
func notifyIdle(message string) tea.Cmd {
	return func() tea.Msg {
		_, _ = fmt.Fprint(os.Stdout, "\a", osc9Notify(message))
		return nil
	}
}

// osc9Notify formats an OSC 9 desktop-notification escape sequence.
func osc9Notify(message string) string {
	return "\x1b]9;" + message + "\x07"
}
