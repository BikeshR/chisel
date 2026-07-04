package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/session"
)

// handleSessionsCommand implements /sessions — lists every saved session
// for the current working directory, most recent first (same numbering
// /resume <n> and /rewind <n> both use), marking whichever one is
// currently active. Read-only; /resume does the actual switching.
func (m Model) handleSessionsCommand() Model {
	metas, err := session.List(m.workDir)
	if err != nil {
		m.appendLine(errorStyle.Render("couldn't list sessions: " + err.Error()))
		return m
	}
	if len(metas) == 0 {
		m.appendLine(dimStyle.Render("no saved sessions yet for this directory"))
		return m
	}

	var b strings.Builder
	b.WriteString("sessions for this directory (most recent first):\n")
	for i, meta := range metas {
		current := ""
		if meta.ID == m.sessionID {
			current = "  (current)"
		}
		fmt.Fprintf(&b, "  %d. %s — %d message(s), saved %s%s\n", i+1, meta.Title, meta.MessageCount, humanizeSince(meta.SavedAt), current)
	}
	b.WriteString("type /resume <n> to switch")
	m.appendLine(dimStyle.Render(b.String()))
	return m
}

// handleResumeCommand implements /resume [n] — /resume alone lists
// sessions exactly like /sessions; /resume <n> switches this running
// conversation to that saved session. Restricted to stateInput, the
// same reasoning /rewind's own restriction has: switching conversation
// history mid-turn (a pending tool call, an in-flight stream) has no
// sane meaning.
func (m Model) handleResumeCommand(args []string) (Model, tea.Cmd) {
	if m.state != stateInput {
		m.appendLine(errorStyle.Render("can't resume while a turn is in progress — press esc first"))
		return m, nil
	}
	if len(args) == 0 {
		return m.handleSessionsCommand(), nil
	}

	metas, err := session.List(m.workDir)
	if err != nil {
		m.appendLine(errorStyle.Render("couldn't list sessions: " + err.Error()))
		return m, nil
	}

	n, convErr := strconv.Atoi(args[0])
	if convErr != nil || n < 1 || n > len(metas) {
		m.appendLine(errorStyle.Render(fmt.Sprintf(
			"usage: /resume <n> — %d session(s) available; /sessions (or /resume alone) lists them", len(metas))))
		return m, nil
	}

	target := metas[n-1]
	if target.ID == m.sessionID {
		m.appendLine(dimStyle.Render("already on that session"))
		return m, nil
	}

	messages, _, ok := session.LoadByID(m.workDir, target.ID)
	if !ok {
		m.appendLine(errorStyle.Render("couldn't load that session"))
		return m, nil
	}

	m.sessionID = target.ID
	m.messages = messages
	// Same stale-checkpoint-index hazard /new and /compact already guard
	// against (see their own doc comments) — a checkpoint's messageIndex
	// refers to a position in the conversation just replaced, and the
	// current todo list almost certainly doesn't apply to a different
	// saved conversation either. lastToolResultIdx/the doom-loop counters
	// are the same story: both describe the conversation just left
	// behind, not this one.
	m.todos = nil
	m.checkpoints = nil
	m.pendingRewind = nil
	m.lastContextTokens = 0
	m.lastToolResultIdx = -1
	m.lastToolCallKey = ""
	m.toolCallRepeatCount = 0
	m.recomputeViewportHeight()

	m.entries = renderHistory(messages)
	m.appendLine(dimStyle.Render(fmt.Sprintf("── resumed %q ──", target.Title)))
	m.refreshAndMaybeStickToBottom()

	// Re-saved immediately (even though target.ID's content on disk is
	// already exactly this) so its SavedAt becomes the newest — without
	// this, LoadLatest would keep resuming whichever session was most
	// recently *saved*, not the one just switched to, on the next launch.
	return m, saveSession(m.workDir, m.sessionID, m.messages)
}
