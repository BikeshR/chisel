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

// handleBangResult renders a bang command's output — capped by the same
// TruncateOutput a tool result gets, not shown truly unbounded; unlike a
// tool result's single-line-for-the-model cap, though, the full (if
// large) output up to that cap is shown, since the user asked for this
// directly and expects to see close to what a real shell would print.
func (m Model) handleBangResult(msg bangResultMsg) (Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput

	if msg.err != nil {
		m.appendLine(errorStyle.Render(interruptibleErrorText(msg.err)))
	} else if msg.output != "" {
		m.appendLine(agent.TruncateOutput(msg.output))
	}

	// A bang command can change git state (checkout, commit, stash) just
	// as easily as a tool call can — refresh the cached status-bar
	// segment rather than leaving it stale until the next model turn.
	flush := m.flushPendingBackgroundResults()
	return m, tea.Batch(flush, m.dequeueOrSubmit(), refreshGitStatus(m.workDir))
}
