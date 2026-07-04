package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/gitutil"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 4 // input line + status bar + margin
		m.textInput.Width = msg.Width - 2
		// Re-wrap every entry to the new width — without this, the
		// viewport's own hard truncation (not wrapping) at msg.Width
		// would otherwise just cut off anything wider than the new
		// terminal, silently dropping content rather than reflowing it.
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamEventMsg:
		return m.handleStreamEvent(msg)

	case toolResultMsg:
		return m.handleToolResult(msg.result)

	case modelCheckResultMsg:
		return m.handleModelCheckResult(msg)

	case compactResultMsg:
		return m.handleCompactResult(msg)

	case sessionSaveErrorMsg:
		m.appendLine(errorStyle.Render("session save failed: " + msg.err.Error()))
		return m, nil

	case autoCommitResultMsg:
		if msg.err != nil {
			m.appendLine(errorStyle.Render("auto-commit failed: " + msg.err.Error()))
		} else if msg.sha != "" {
			m.appendLine(dimStyle.Render("→ committed " + msg.sha))
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		return m, tea.Quit
	}

	switch m.state {
	case stateAwaitingPermission:
		// stateAwaitingPermission is only ever entered with a non-empty
		// pendingUses (dispatchNextTool sets both together), but that's a
		// convention enforced by control flow, not the type system — a
		// future change to how this state gets entered could violate it,
		// and indexing [0] below would then panic instead of just no-oping.
		if len(m.pendingUses) == 0 {
			m.state = stateInput
			return m, nil
		}
		switch msg.String() {
		case "y", "Y", "enter":
			call := m.pendingUses[0]
			m.state = stateExecutingTool
			m.appendLine(dimStyle.Render("  → approved"))
			return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, call)
		case "n", "N", "esc":
			return m.handleToolResult(agent.ToolResult{
				ID:      m.pendingUses[0].ID,
				Content: "The user denied permission for this action.",
				IsError: true,
			})
		}
		return m, nil

	case stateInput:
		if msg.Type == tea.KeyEnter {
			return m.submit()
		}
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd

	case stateWaitingModel, stateExecutingTool:
		// Busy: every other keystroke is ignored, but esc aborts whatever
		// is running — an in-flight model request or tool call — via the
		// context newTurnContext stashed when it started. The operation's
		// own error path (handleStreamEvent, handleToolResult via
		// executeTool's result) picks up from there and returns to
		// stateInput; esc itself doesn't touch state.
		if msg.Type == tea.KeyEsc && m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, nil

	default:
		return m, nil
	}
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	text := m.textInput.Value()
	if text == "" {
		return m, nil
	}
	m.textInput.Reset()

	if strings.HasPrefix(text, "/") {
		var cmd tea.Cmd
		m, cmd = m.handleCommand(text)
		return m, tea.Batch(cmd, textinput.Blink)
	}

	if m.autoCommit {
		// Snapshot before this turn's actions, not just before this
		// message — so /git auto's eventual commit can be scoped to
		// what changed during it, excluding anything the user already
		// had unstaged. Best-effort: if it fails, preTurnDirty stays
		// nil, which CommitNewlyChanged treats as "nothing was already
		// dirty" — a fresh session's zero value behaves the same way.
		m.preTurnDirty, _ = gitutil.DirtyPaths(m.workDir)
	}

	m.messages = append(m.messages, agent.Message{Role: "user", Content: text})
	m.appendLine(userStyle.Render("you  ") + text)
	m.state = stateWaitingModel

	ctx := m.newTurnContext()
	return m, tea.Batch(startStream(ctx, m.client, m.messages), saveSession(m.workDir, m.messages), textinput.Blink)
}

// handleStreamEvent processes one event from the in-flight response. While
// the stream is still going it renders text deltas live and re-arms the
// listener; once done, it hands the fully accumulated message off to
// handleStreamComplete.
func (m Model) handleStreamEvent(msg streamEventMsg) (tea.Model, tea.Cmd) {
	ev := msg.event

	if ev.TextDelta != "" {
		m.appendStreamText(ev.TextDelta)
	}

	if !ev.Done {
		return m, waitForChunk(msg.ch)
	}

	if ev.Err != nil {
		m.endTurn()
		m.err = ev.Err
		m.endStreamLine()
		m.appendLine(errorStyle.Render("error  " + interruptibleErrorText(ev.Err)))
		m.state = stateInput
		// No notification for a cancellation — esc means the user is
		// already at the keyboard, mid-keypress; a genuine error while
		// they may have stepped away is the case worth surfacing.
		if errors.Is(ev.Err, context.Canceled) {
			return m, nil
		}
		return m, notifyIdle("chisel hit an error")
	}

	return m.handleStreamComplete(*ev.Message, ev.Usage)
}

func (m Model) handleStreamComplete(resp agent.Message, usage agent.Usage) (tea.Model, tea.Cmd) {
	m.endStreamLine()

	m.messages = append(m.messages, resp)
	m.tokensIn += usage.InputTokens
	m.tokensOut += usage.OutputTokens
	m.lastContextTokens = usage.InputTokens
	// Go by whether the message actually has tool calls, not the
	// provider's finish_reason field — a provider can (and does, in
	// practice) report finish_reason: "stop" while still returning a
	// non-empty tool_calls array, and trusting finish_reason there would
	// silently skip dispatching them.
	m.pendingUses = resp.ToolCalls

	if len(m.pendingUses) == 0 {
		m.endTurn()
		m.state = stateInput
		save := saveSession(m.workDir, m.messages)
		notify := notifyIdle("chisel is done")
		if m.autoCommit {
			return m, tea.Batch(save, notify, autoCommit(m.workDir, m.preTurnDirty, lastUserText(m.messages)))
		}
		return m, tea.Batch(save, notify)
	}

	// Deliberately not saving here: resp (just appended above) carries
	// tool_calls with no matching "tool" result messages yet, and that's
	// an invalid history to persist — every future request replaying a
	// session saved in this state gets rejected by the API. handleToolResult
	// saves once every pending call is actually resolved (executed,
	// denied, or blocked), whichever comes first.
	return m.dispatchNextTool()
}

// dispatchNextTool looks at the front of the pending tool-use queue and
// either asks for permission or runs it immediately.
func (m Model) dispatchNextTool() (tea.Model, tea.Cmd) {
	// Every current caller only reaches here with a non-empty queue
	// (handleStreamComplete and handleToolResult both check first), but
	// that's enforced by their control flow, not by this function itself
	// — guard it directly rather than relying on callers to keep doing so.
	if len(m.pendingUses) == 0 {
		m.endTurn()
		m.state = stateInput
		return m, nil
	}
	call := m.pendingUses[0]

	// Plan mode hard-denies anything that would otherwise need permission
	// — not just a prompt-level instruction the model might ignore. A
	// call that's already auto-allowed (glob, grep, editor view) is
	// read-only by definition and stays allowed; that's the whole point
	// of "read-only planning".
	if needsPermission(call) && m.client.PlanMode() {
		m.appendLine(errorStyle.Render("✗ blocked (plan mode): " + summarizeCall(call)))
		return m.handleToolResult(agent.ToolResult{
			ID:      call.ID,
			Content: "Not run — chisel is in plan mode, which only allows read-only exploration. Describe this as part of your plan instead, then stop; the user will exit plan mode before you make any changes.",
			IsError: true,
		})
	}

	if needsPermission(call) {
		m.endTurn() // nothing async is in flight while waiting on a y/n decision
		m.state = stateAwaitingPermission
		prompt := fmt.Sprintf("allow %s?  [y/n]", summarizeCall(call))
		if diff, ok := agent.PreviewEdit(m.workDir, call); ok {
			prompt = fmt.Sprintf("allow %s?\n\n%s\n[y/n]", summarizeCall(call), strings.TrimRight(diff, "\n"))
		}
		m.appendLine(permissionStyle.Render(prompt))
		return m, notifyIdle("chisel needs your permission")
	}

	m.state = stateExecutingTool
	m.appendLine(toolStyle.Render("  " + summarizeCall(call)))
	return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, call)
}

func (m Model) handleToolResult(result agent.ToolResult) (tea.Model, tea.Cmd) {
	if len(m.pendingUses) == 0 {
		return m, nil
	}

	if result.IsError {
		m.appendLine(errorStyle.Render("  ✗ " + firstLine(interruptibleResultText(result.Content))))
	} else {
		m.appendLine(dimStyle.Render("  ✓ " + firstLine(result.Content)))
	}

	m.pendingResults = append(m.pendingResults, result.ToMessage())
	m.pendingUses = m.pendingUses[1:]

	if len(m.pendingUses) > 0 {
		return m.dispatchNextTool()
	}

	m.messages = append(m.messages, m.pendingResults...)
	m.pendingResults = nil
	m.state = stateWaitingModel
	ctx := m.newTurnContext()
	return m, tea.Batch(startStream(ctx, m.client, m.messages), saveSession(m.workDir, m.messages))
}

// interruptibleResultText mirrors interruptibleErrorText for a tool
// result's content — by the time an error reaches here it's already
// been flattened to a plain string (agent.ToolResult.Content), so this
// checks for context.Canceled's exact message rather than errors.Is.
func interruptibleResultText(content string) string {
	if content == context.Canceled.Error() {
		return "interrupted"
	}
	return content
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i] // safe: i is the byte offset of a single-byte '\n', always a rune boundary
		}
	}
	if truncated, ok := truncateRunes(s, 120); ok {
		return truncated + "…"
	}
	return s
}
