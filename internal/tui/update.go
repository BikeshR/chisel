package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/gitutil"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.recomputeViewportHeight()
		m.textArea.SetWidth(msg.Width - 2)
		// Re-wrap every entry to the new width — without this, the
		// viewport's own hard truncation (not wrapping) at msg.Width
		// would otherwise just cut off anything wider than the new
		// terminal, silently dropping content rather than reflowing it.
		m.refreshAndMaybeStickToBottom()
		return m, nil

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

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

	case checkpointCreatedMsg:
		return m.handleCheckpointCreated(msg), nil

	case backgroundTaskStartedMsg:
		return m.handleBackgroundTaskStarted(msg), nil

	case backgroundTaskDoneMsg:
		return m.handleBackgroundTaskDone(msg)

	case bangResultMsg:
		return m.handleBangResult(msg)

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

	if m.handleScrollKey(msg) {
		return m, nil
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
		case "y", "Y":
			// Deliberately not also bound to enter — a permission prompt
			// following right after typing and hitting enter to submit
			// the message that triggered it is exactly the pattern that
			// makes an enter-approves binding dangerous: a reflexive
			// enter can approve a bash command the user never actually read.
			call := m.pendingUses[0]
			m.state = stateExecutingTool
			m.appendLine(dimStyle.Render("  → approved"))
			return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
		case "a", "A":
			call := m.pendingUses[0]
			if key, ok := allowlistKey(call); ok {
				if m.sessionAllowlist == nil {
					m.sessionAllowlist = make(map[string]bool)
				}
				m.sessionAllowlist[key] = true
			}
			m.state = stateExecutingTool
			m.appendLine(dimStyle.Render("  → approved (always allow for this session)"))
			return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
		case "n", "N":
			// Denying isn't a dead end: rather than immediately resending
			// a canned "denied" message and letting the model guess why,
			// drop back to stateInput with the denial queued — whatever
			// the user types next (or nothing) becomes the reason fed
			// back, resolved by submit().
			m.state = stateInput
			m.awaitingDenialReason = true
			m.appendLine(dimStyle.Render("  → denied — say what to do instead, or press enter to just deny"))
			return m, nil
		case "esc":
			// A blunter, immediate deny — no reason prompt — for when the
			// user just wants out, matching esc's meaning elsewhere in
			// the app (abandon this, don't ask more questions about it).
			return m.handleToolResult(agent.ToolResult{
				ID:      m.pendingUses[0].ID,
				Content: "The user denied permission for this action.",
				IsError: true,
			})
		}
		return m, nil

	case stateInput:
		// Plain enter submits; alt+enter (textArea's rebound
		// InsertNewline — see New) falls through instead, so it never
		// reaches this branch at all.
		if msg.Type == tea.KeyEnter && !msg.Alt {
			return m.submit()
		}
		if msg.Type == tea.KeyTab {
			m.completeFileReferenceInInput()
			return m, nil
		}
		var cmd tea.Cmd
		m.textArea, cmd = m.textArea.Update(msg)
		return m, cmd

	case stateWaitingModel, stateExecutingTool:
		// Busy, but not deaf: esc aborts whatever is running — an
		// in-flight model request or tool call — via the context
		// newTurnContext stashed when it started. The operation's own
		// error path (handleStreamEvent, handleToolResult via
		// executeTool's result) picks up from there and returns to
		// stateInput; esc itself doesn't touch state.
		if msg.Type == tea.KeyEsc && m.cancelTurn != nil {
			m.cancelTurn()
			return m, nil
		}
		// Enter here doesn't submit (there's no turn to submit into
		// yet) — it queues instead, delivered in order by
		// dequeueOrSubmit once chisel is next free to send something,
		// rather than being swallowed the way every keystroke while
		// busy used to be.
		if msg.Type == tea.KeyEnter && !msg.Alt {
			text := strings.TrimSuffix(m.textArea.Value(), "\n")
			m.textArea.Reset()
			if text != "" {
				m.queuedMessages = append(m.queuedMessages, text)
				m.appendLine(dimStyle.Render("  → queued: " + firstLine(text)))
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.textArea, cmd = m.textArea.Update(msg)
		return m, cmd

	default:
		return m, nil
	}
}

// handleScrollKey intercepts transcript-scrolling keys before any
// state-specific handling below gets a chance to swallow them — a long
// permission-prompt diff or an in-progress response both need to stay
// scrollable, not just the idle input state. PgUp/PgDown are always
// routed (nothing else binds them); ctrl+u/ctrl+d are only intercepted
// outside stateInput, where they're textinput's own delete-to-cursor
// bindings instead — reusing the viewport's own keymap-driven Update
// here would also eat plain letters like "j"/"k"/"f"/"b" while typing,
// so scrolling calls the viewport's specific methods directly rather
// than routing the raw KeyMsg through it.
func (m *Model) handleScrollKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyPgUp:
		m.viewport.PageUp()
		return true
	case tea.KeyPgDown:
		m.viewport.PageDown()
		return true
	case tea.KeyCtrlU:
		if m.state != stateInput {
			m.viewport.HalfPageUp()
			return true
		}
	case tea.KeyCtrlD:
		if m.state != stateInput {
			m.viewport.HalfPageDown()
			return true
		}
	}
	return false
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSuffix(m.textArea.Value(), "\n")
	m.textArea.Reset()

	// A denial from the permission prompt is waiting on a reason (or an
	// explicit "no reason" via an empty submission) — resolve it here
	// rather than treating this as a fresh message, regardless of
	// whether text is empty or looks like a "/" command.
	if m.awaitingDenialReason {
		m.awaitingDenialReason = false
		if len(m.pendingUses) == 0 {
			return m, nil
		}
		reason := "The user denied permission for this action."
		if text != "" {
			reason += " " + text
		}
		return m.handleToolResult(agent.ToolResult{ID: m.pendingUses[0].ID, Content: reason, IsError: true})
	}

	if text == "" {
		return m, nil
	}

	if strings.HasPrefix(text, "/") {
		var cmd tea.Cmd
		m, cmd = m.handleCommand(text)
		return m, tea.Batch(cmd, textarea.Blink)
	}

	if strings.HasPrefix(text, "!") {
		var cmd tea.Cmd
		m, cmd = m.runBang(strings.TrimPrefix(text, "!"))
		return m, tea.Batch(cmd, textarea.Blink)
	}

	m, cmd := m.submitText(text)
	return m, tea.Batch(cmd, textarea.Blink)
}

// submitText sends text as a new user message and starts the model's
// response — the shared core of a normal submit() and of delivering a
// message that was queued while busy (see dequeueOrSubmit).
func (m Model) submitText(text string) (Model, tea.Cmd) {
	if m.autoCommit {
		// Snapshot before this turn's actions, not just before this
		// message — so /git auto's eventual commit can be scoped to
		// what changed during it, excluding anything the user already
		// had unstaged. Best-effort: if it fails, preTurnDirty stays
		// nil, which CommitNewlyChanged treats as "nothing was already
		// dirty" — a fresh session's zero value behaves the same way.
		m.preTurnDirty, _ = gitutil.DirtyPaths(m.workDir)
	}

	m.syncMCPHealth()
	m.pendingRewind = nil // a new turn starting cancels any pending /rewind confirmation

	messageIndex := len(m.messages)

	// The model sees @path expanded to the file's actual content; the
	// transcript shows exactly what the user typed — otherwise a large
	// injected file would turn the display into a wall of text every
	// time one's referenced.
	m.messages = append(m.messages, agent.Message{Role: "user", Content: expandFileReferences(m.workDir, text)})
	m.appendLine(userStyle.Render("you  ") + text)
	m.state = stateWaitingModel

	ctx := m.newTurnContext()
	return m, tea.Batch(
		startStream(ctx, m.client, m.messages),
		saveSession(m.workDir, m.messages),
		checkpointCmd(m.checkpointStore, firstLine(text), messageIndex),
	)
}

// dequeueOrSubmit delivers the next message queued while chisel was
// busy (see the stateWaitingModel/stateExecutingTool case in handleKey),
// if any — call after any transition back to stateInput, batched
// alongside whatever else that transition already needed to do (saving
// the session, notifying), so a message typed while busy isn't silently
// swallowed once the wait is finally over. Returns nil if nothing is queued.
func (m *Model) dequeueOrSubmit() tea.Cmd {
	if len(m.queuedMessages) == 0 {
		return nil
	}
	next := m.queuedMessages[0]
	m.queuedMessages = m.queuedMessages[1:]
	updated, cmd := m.submitText(next)
	*m = updated
	return cmd
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
		m.endStreamLine()
		m.appendLine(errorStyle.Render("error  " + interruptibleErrorText(ev.Err)))
		m.state = stateInput
		if queued := m.dequeueOrSubmit(); queued != nil {
			return m, queued
		}
		// No notification for a cancellation — esc means the user is
		// already at the keyboard, mid-keypress; a genuine error while
		// they may have stepped away is the case worth surfacing.
		if errors.Is(ev.Err, context.Canceled) {
			return m, nil
		}
		return m, notifyIdle("chisel hit an error")
	}

	return m.handleStreamComplete(*ev.Message, ev.Usage, ev.FinishReason)
}

// handleStreamComplete's finishReason param is display-only — shown as
// a "response truncated" notice when it's "length", nothing more.
// Whether to dispatch tool calls is still decided purely from
// resp.ToolCalls (see the comment below): trusting finish_reason for
// that specifically was the bug this same function was fixed for
// earlier — a provider can (and does, in practice) report finish_reason:
// "stop" while still returning a non-empty tool_calls array.
func (m Model) handleStreamComplete(resp agent.Message, usage agent.Usage, finishReason string) (tea.Model, tea.Cmd) {
	m.endStreamLine()

	m.messages = append(m.messages, resp)
	m.tokensIn += usage.InputTokens
	m.tokensOut += usage.OutputTokens
	m.lastContextTokens = usage.InputTokens
	if finishReason == "length" {
		m.appendLine(dimStyle.Render("(response truncated — hit the model's length limit)"))
	}
	m.pendingUses = resp.ToolCalls

	if len(m.pendingUses) == 0 {
		m.endTurn()
		m.state = stateInput
		save := saveSession(m.workDir, m.messages)

		// A queued message delivered here means chisel is about to be
		// busy again right away — skip the "chisel is done" notification
		// in that case, the user's already at the keyboard (that's how
		// the message got queued in the first place).
		queued := m.dequeueOrSubmit()
		var notify tea.Cmd
		if queued == nil {
			notify = notifyIdle("chisel is done")
		}

		var autoCommitCmd tea.Cmd
		if m.autoCommit {
			autoCommitCmd = autoCommit(m.workDir, m.preTurnDirty, lastUserText(m.messages))
		}

		// Auto-compact once the context is large enough that the status
		// bar would already be warning about it — same threshold, just
		// acted on instead of left for the user to notice and type
		// /compact themselves. Only when genuinely idle (nothing
		// queued): compacting is itself a turn, and a queued message
		// means the user's already mid-flow and shouldn't be interrupted
		// by an extra step in between.
		if queued == nil && m.lastContextTokens >= contextWarnThreshold {
			m.appendLine(dimStyle.Render("context is large — compacting automatically…"))
			m.state = stateWaitingModel
			return m, tea.Batch(save, notify, autoCommitCmd, compact(m.newTurnContext(), m.client, m.messages))
		}

		return m, tea.Batch(save, notify, queued, autoCommitCmd)
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
		return m, m.dequeueOrSubmit()
	}
	call := m.pendingUses[0]

	switch decision, reason := decidePermission(call, m.client.PlanMode(), m.sessionAllowlist); decision {
	case permissionDeny:
		return m.handleToolResult(agent.ToolResult{ID: call.ID, Content: reason, IsError: true})

	case permissionAsk:
		m.endTurn() // nothing async is in flight while waiting on a y/n decision
		m.state = stateAwaitingPermission

		options := "[y/n]"
		if _, allowlistable := allowlistKey(call); allowlistable {
			options = "[y/n/a]"
		}

		prompt := fmt.Sprintf("allow %s?  %s", summarizeCall(call), options)
		if diff, ok := agent.PreviewEdit(m.workDir, call); ok {
			prompt = fmt.Sprintf("allow %s?\n\n%s\n%s", summarizeCall(call), colorizeDiff(diff), options)
		} else if args := mcpCallArgsPreview(call); args != "" {
			prompt = fmt.Sprintf("allow %s?\n\n%s\n%s", summarizeCall(call), args, options)
		} else if call.Function.Name == "bash" && m.bash != nil {
			if cwd := m.bash.Cwd(); cwd != "" && cwd != m.workDir {
				prompt = fmt.Sprintf("allow %s?  (in %s)  %s", summarizeCall(call), cwd, options)
			}
		}
		m.appendLine(permissionStyle.Render(prompt))
		return m, notifyIdle("chisel needs your permission")

	default: // permissionAllow
		m.state = stateExecutingTool
		m.appendLine(toolStyle.Render("  " + summarizeCall(call)))
		return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
	}
}

func (m Model) handleToolResult(result agent.ToolResult) (tea.Model, tea.Cmd) {
	if len(m.pendingUses) == 0 {
		return m, nil
	}

	// Non-zero only for tools that make their own model requests under
	// the hood (dispatch_subagent) — without this, a subagent's real
	// cost, often the priciest single call in a turn since it's
	// multi-turn on its own, never showed up in the status bar's totals.
	m.tokensIn += result.Usage.InputTokens
	m.tokensOut += result.Usage.OutputTokens

	if result.IsError {
		m.appendLine(errorStyle.Render("  ✗ " + firstLine(interruptibleResultText(result.Content))))
	} else {
		m.appendLine(dimStyle.Render("  ✓ " + firstLine(result.Content)))
		// Extracted from the call's own arguments, not result.Content —
		// runUpdateTodos only returns a short confirmation string. Only
		// on success: a call that failed validation shouldn't replace an
		// otherwise-valid list with partial or malformed data.
		if todos, ok := parseTodos(m.pendingUses[0]); ok {
			m.todos = todos
			m.recomputeViewportHeight()
		}
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
