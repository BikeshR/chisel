package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/hooks"
)

// userPromptHookResultMsg carries the outcome of running UserPromptSubmit
// hooks against a message about to be sent — see checkUserPromptSubmitHooks.
type userPromptHookResultMsg struct {
	text    string
	blocked bool
	reason  string
	err     error
}

// checkUserPromptSubmitHooks runs every configured UserPromptSubmit hook
// against text as its own async Cmd — the same reasoning preToolUse hooks
// already require (see executeTool in model.go): a hook is an arbitrary
// shell command that can take real time, so it can't run synchronously on
// the Update goroutine the way the rest of dispatchText's routing does.
func checkUserPromptSubmitHooks(ctx context.Context, workDir string, list []hooks.Hook, text string) tea.Cmd {
	return func() tea.Msg {
		blocked, reason, err := hooks.RunUserPromptSubmit(ctx, workDir, list, text)
		return userPromptHookResultMsg{text: text, blocked: blocked, reason: reason, err: err}
	}
}

// submitTextWithHookCheck is submitText, but first running any
// configured UserPromptSubmit hooks against text — shared by every path
// that sends new, non-command text to the model (a real typed message
// via dispatchText, a custom command's expanded template, a /goal
// auto-continuation), so a hook can't be bypassed just by going through
// one of those instead of a plain typed message. Deliberately separate
// from dispatchText's own "/" and "!" routing: text here is often
// already-expanded/derived (a custom command's template, a goal
// continuation), and re-running slash/bang detection against it could
// misroute content that merely starts with one of those characters.
func (m Model) submitTextWithHookCheck(text string) (Model, tea.Cmd) {
	if len(m.hooks.Hooks.UserPromptSubmit) > 0 {
		// Provisionally busy before it's known whether this message will
		// actually be sent — handleUserPromptHookResult either proceeds
		// into the real submitText (which re-enters stateWaitingModel
		// itself, harmlessly) or reverts back to stateInput if a hook
		// blocks it.
		m.startBusy(stateWaitingModel)
		return m, checkUserPromptSubmitHooks(m.newTurnContext(), m.workDir, m.hooks.Hooks.UserPromptSubmit, text)
	}
	return m.submitText(text)
}

// handleUserPromptHookResult either proceeds to actually submit text (no
// hook blocked it, or one errored — an error means the hook itself is
// broken, not that the message was judged unsafe, so it fails open the
// same way a broken preToolUse hook does) or reverts the provisional busy
// state dispatchText already entered and reports why it was blocked.
func (m Model) handleUserPromptHookResult(msg userPromptHookResultMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(errorStyle.Render("userPromptSubmit hook: " + msg.err.Error()))
		return m.submitText(msg.text)
	}
	if msg.blocked {
		m.endTurn()
		m.state = stateInput
		m.appendLine(errorStyle.Render("Blocked by a userPromptSubmit hook: " + msg.reason))
		return m, m.turnSettledCmd()
	}
	return m.submitText(msg.text)
}
