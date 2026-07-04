package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// bangResultMsg carries the output of a "!command" run directly through
// the persistent bash session, bypassing the model entirely.
type bangResultMsg struct {
	output string
	err    error
}

// runBang handles a "!command" line typed at the input — Crush and
// OpenCode both call this "bang mode": the command runs immediately
// through the same persistent BashSession regular bash tool calls use
// (so cd and exported env vars stay consistent between the two),
// skipping the model round-trip entirely for things like `git status`
// or `ls` that don't need the model's involvement at all. No permission
// prompt: this is the user's own direct action, typed by hand, not
// something the model decided to do.
func (m Model) runBang(command string) (Model, tea.Cmd) {
	command = strings.TrimSpace(command)
	if command == "" {
		m.appendLine(errorStyle.Render("! needs a command"))
		return m, nil
	}

	m.appendLine(toolStyle.Render("! " + command))
	m.startBusy(stateExecutingTool)
	ctx := m.newTurnContext()
	bash := m.bash
	return m, func() tea.Msg {
		if bash == nil {
			return bangResultMsg{err: fmt.Errorf("no bash session available")}
		}
		output, err := bash.Run(ctx, command, false)
		return bangResultMsg{output: output, err: err}
	}
}

// handleBangResult renders a bang command's raw output in full — unlike
// a tool result (truncated to one line for the model's benefit), the
// user asked for this directly and expects to see all of it, the same
// as running it in a real shell.
func (m Model) handleBangResult(msg bangResultMsg) (Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput

	if msg.err != nil {
		m.appendLine(errorStyle.Render(interruptibleErrorText(msg.err)))
	} else if msg.output != "" {
		m.appendLine(agent.TruncateOutput(msg.output))
	}

	return m, m.dequeueOrSubmit()
}
