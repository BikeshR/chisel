package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/history"
	"github.com/BikeshR/chisel/internal/permrules"
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
		return m.handleMouseMsg(msg)

	case clearClipboardOSCMsg:
		m.pendingClipboardOSC = ""
		return m, nil

	case notifyIdleMsg:
		m.pendingNotifyOSC = "\a" + osc9Notify(msg.message)
		return m, clearNotifyOSCCmd()

	case clearNotifyOSCMsg:
		m.pendingNotifyOSC = ""
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamEventMsg:
		return m.handleStreamEvent(msg)

	case toolResultMsg:
		return m.handleToolResult(msg.result, msg.interrupted)

	case modelCheckResultMsg:
		return m.handleModelCheckResult(msg)

	case compactResultMsg:
		return m.handleCompactResult(msg)

	case sessionSaveErrorMsg:
		m.appendLine(errorStyle.Render("session save failed: " + msg.err.Error()))
		return m, nil

	case historySaveErrorMsg:
		m.appendLine(dimStyle.Render("note: couldn't save prompt history: " + msg.err.Error()))
		return m, nil

	case gitStatusMsg:
		m.gitIsRepo = msg.isRepo
		m.gitBranch = msg.branch
		m.gitDirty = msg.dirty
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

	case editorDoneMsg:
		return m.handleEditorDone(msg)

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

	// ctrl+o isn't bound by textarea's own default keymap, and expanding
	// the last result is useful regardless of what else is going on
	// (composing the next message, waiting on a permission decision), so
	// it's handled here rather than only within stateInput.
	if msg.Type == tea.KeyCtrlO {
		m.toggleLastToolResult()
		return m, nil
	}

	// ctrl+x (also unbound by default) opens $EDITOR on whatever's
	// currently in the textarea — available whenever there's a textarea
	// to edit, i.e. anything except the permission prompt's y/n/a.
	if msg.Type == tea.KeyCtrlX && m.state != stateAwaitingPermission {
		return m, startExternalEditor(m.textArea.Value())
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
			m.startBusy(stateExecutingTool)
			m.appendLine(dimStyle.Render("  → approved"))
			return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
		case "a", "A":
			call := m.pendingUses[0]
			// A doom-loop-forced prompt never offers "a" (see
			// dispatchNextTool), but the key itself isn't otherwise
			// disabled — without this check, a habitual "a" here would
			// silently permit every future repeat of a call actively
			// suspected of looping, exactly what the guard exists to
			// prevent. Falls through to a plain approval instead.
			if key, ok := allowlistKey(call); ok && !m.awaitingLoopConfirmation {
				if m.sessionAllowlist == nil {
					m.sessionAllowlist = make(map[string]bool)
				}
				m.sessionAllowlist[key] = true
			}
			m.startBusy(stateExecutingTool)
			m.appendLine(dimStyle.Render("  → approved (always allow for this session)"))
			return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
		case "p", "P":
			// Same doom-loop guard as "a" — a forced confirmation never
			// offers this option (see dispatchNextTool), but the key
			// itself isn't otherwise disabled, so a habitual press still
			// just falls through to a plain approval instead of writing a
			// permanent rule for a call actively suspected of looping.
			call := m.pendingUses[0]
			toolName, pattern, ok := persistableRuleFor(call)
			switch {
			case ok && !m.awaitingLoopConfirmation:
				// Re-read from disk rather than building on m.permRules —
				// that in-memory copy can be nil (trust was declined at
				// startup, or the file simply parse-errored then) or stale
				// (edited on disk mid-session), and Add+Save on top of
				// either would silently overwrite whatever's actually on
				// disk with just this one new rule, destroying an
				// existing repo-provided policy in the process.
				onDisk, _, loadErr := permrules.Load(m.workDir)
				if loadErr != nil {
					m.appendLine(errorStyle.Render("  couldn't save permission rule: " + loadErr.Error()))
					break
				}
				updated := permrules.Add(onDisk, toolName, pattern, permrules.Allow)
				if err := permrules.Save(m.workDir, updated); err != nil {
					m.appendLine(errorStyle.Render("  couldn't save permission rule: " + err.Error()))
				} else {
					m.permRules = updated
					// Auto-trust the file this session just wrote — the
					// keypress that created this rule *is* the human
					// approval the trust gate exists to require, so a
					// future run loading this same content shouldn't have
					// to ask again for something the user just did themselves.
					if data, readErr := os.ReadFile(permrules.ConfigPath(m.workDir)); readErr == nil {
						_ = permrules.Trust(permrules.ContentHash(data))
					}
					m.appendLine(dimStyle.Render("  → approved (saved as a permanent rule in .chisel/permissions.json)"))
				}
			default:
				m.appendLine(dimStyle.Render("  → approved"))
			}
			m.startBusy(stateExecutingTool)
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
			}, false)
		}
		return m, nil

	case stateInput:
		if m.reverseSearchActive {
			return m.handleReverseSearchKey(msg)
		}
		if msg.Type == tea.KeyCtrlR {
			m.startReverseSearch()
			return m, nil
		}
		// Plain enter submits; alt+enter (textArea's rebound
		// InsertNewline — see New) falls through instead, so it never
		// reaches this branch at all.
		if msg.Type == tea.KeyEnter && !msg.Alt {
			return m.submit()
		}
		if msg.Type == tea.KeyTab {
			// A slash command is only ever the first (and, while still
			// being typed, only) token — once a space follows it, tab
			// completion is back to @-file references, same as anywhere
			// else in the message.
			if value := strings.TrimRight(m.textArea.Value(), "\n"); strings.HasPrefix(value, "/") && !strings.ContainsAny(value, " \t\n") {
				m.completeCommandInInput()
			} else {
				m.completeFileReferenceInInput()
			}
			return m, nil
		}
		// Only take over up/down for history recall when the textarea is
		// a single line — otherwise these are the textarea's own cursor
		// movement between lines of a multi-line draft, which must win.
		if (msg.Type == tea.KeyUp || msg.Type == tea.KeyDown) && m.textArea.LineCount() == 1 {
			m.navigateHistory(msg.Type == tea.KeyUp)
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
			var historyCmd tea.Cmd
			if text != "" {
				m.queuedMessages = append(m.queuedMessages, text)
				historyCmd = m.recordHistory(text)
				m.appendLine(dimStyle.Render("  → queued: " + firstLine(text)))
			}
			return m, historyCmd
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

// recordHistory appends text to the recall history for up/down (see
// navigateHistory) and ctrl+r reverse search, skipping an exact repeat
// of the last entry so repeatedly recalling and resubmitting the same
// line doesn't pile up duplicates — same behavior as a shell's history.
// Also resets navigation state: whatever was being recalled is now
// submitted, so there's nothing left to return to via "down". Returns a
// Cmd that persists the entry to disk (internal/history) so recall
// survives a restart, same as session resume already does for the
// conversation itself — nil if this was a duplicate and there's nothing
// new to persist.
func (m *Model) recordHistory(text string) tea.Cmd {
	m.historyIdx = -1
	m.historyDraft = ""
	if len(m.inputHistory) > 0 && m.inputHistory[len(m.inputHistory)-1] == text {
		return nil
	}
	m.inputHistory = append(m.inputHistory, text)
	return func() tea.Msg {
		if err := history.Append(text); err != nil {
			return historySaveErrorMsg{err: err}
		}
		return nil
	}
}

// navigateHistory moves through inputHistory on up/down, stashing the
// in-progress draft on the first "up" so "down" can restore it once
// navigation passes the most recent entry — mirroring shell history
// recall. A no-op if there's no history yet.
func (m *Model) navigateHistory(up bool) {
	if len(m.inputHistory) == 0 {
		return
	}
	if up {
		switch {
		case m.historyIdx == -1:
			m.historyDraft = m.textArea.Value()
			m.historyIdx = len(m.inputHistory) - 1
		case m.historyIdx > 0:
			m.historyIdx--
		default:
			return // already at the oldest entry
		}
		m.textArea.SetValue(m.inputHistory[m.historyIdx])
	} else {
		if m.historyIdx == -1 {
			return
		}
		m.historyIdx++
		if m.historyIdx >= len(m.inputHistory) {
			m.historyIdx = -1
			m.textArea.SetValue(m.historyDraft)
		} else {
			m.textArea.SetValue(m.inputHistory[m.historyIdx])
		}
	}
	m.textArea.CursorEnd()
}

// startReverseSearch enters ctrl+r's incremental reverse-search mode —
// a no-op if there's no history to search at all.
func (m *Model) startReverseSearch() {
	if len(m.inputHistory) == 0 {
		return
	}
	m.reverseSearchActive = true
	m.reverseSearchQuery = ""
	m.reverseSearchMatchIdx = -1
}

// handleReverseSearchKey drives ctrl+r's incremental search once
// active — mirrors a shell's own reverse-i-search: typing narrows the
// query (searching from the most recent entry backward each time),
// ctrl+r again steps to the next older match for the same query, enter
// accepts the current match and submits it immediately (matching a
// shell's own enter-during-search behavior), esc cancels back to
// whatever was being composed before search started.
func (m Model) handleReverseSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		m.reverseSearchActive = false
		m.reverseSearchQuery = ""
		return m, nil

	case msg.Type == tea.KeyCtrlR:
		m.findReverseSearchMatch(m.reverseSearchMatchIdx - 1)
		return m, nil

	case msg.Type == tea.KeyEnter && !msg.Alt:
		m.reverseSearchActive = false
		found := m.reverseSearchMatchIdx >= 0 && m.reverseSearchMatchIdx < len(m.inputHistory)
		m.reverseSearchQuery = ""
		if !found {
			return m, nil
		}
		m.textArea.SetValue(m.inputHistory[m.reverseSearchMatchIdx])
		return m.submit()

	case msg.Type == tea.KeyBackspace:
		if len(m.reverseSearchQuery) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.reverseSearchQuery)
			m.reverseSearchQuery = m.reverseSearchQuery[:len(m.reverseSearchQuery)-size]
		}
		m.findReverseSearchMatch(len(m.inputHistory) - 1)
		return m, nil

	case len(msg.Runes) > 0:
		m.reverseSearchQuery += string(msg.Runes)
		m.findReverseSearchMatch(len(m.inputHistory) - 1)
		return m, nil
	}
	return m, nil
}

// findReverseSearchMatch searches m.inputHistory from startIdx backward
// (toward index 0, the oldest entry) for one containing the current
// query, setting reverseSearchMatchIdx to what it finds, or -1 if
// nothing in range matches (or the query is empty).
func (m *Model) findReverseSearchMatch(startIdx int) {
	m.reverseSearchMatchIdx = -1
	if m.reverseSearchQuery == "" {
		return
	}
	for i := startIdx; i >= 0; i-- {
		if strings.Contains(m.inputHistory[i], m.reverseSearchQuery) {
			m.reverseSearchMatchIdx = i
			return
		}
	}
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
		return m.handleToolResult(agent.ToolResult{ID: m.pendingUses[0].ID, Content: reason, IsError: true}, false)
	}

	if text == "" {
		return m, nil
	}

	historyCmd := m.recordHistory(text)
	m, cmd := m.dispatchText(text)
	return m, tea.Batch(cmd, historyCmd, textarea.Blink)
}

// dispatchText routes text exactly the way a live submission would — a
// "/" command through handleCommand, a "!" command through runBang,
// anything else through submitText as a plain message. The shared core
// between submit() (which also records history and restarts the cursor
// blink, neither relevant to a message that's already queued) and
// dequeueOrSubmit: a message queued while chisel was busy must be routed
// the same way it would have been if typed and submitted right then —
// dequeueOrSubmit used to send it straight through submitText
// regardless, so a queued "/status" or "!git stash" was delivered to the
// model as a literal user message instead of ever running.
//
// Also where a pending /rewind confirmation actually gets cancelled by
// "anything else," matching what the prompt itself promises ("type
// /rewind confirm to proceed, or anything else to cancel") — a plain
// message already cleared it (submitText, unconditionally, at the start
// of every new turn), but /status, /git, /usage, a bang command, or any
// other command didn't, so a stray /rewind confirm minutes later could
// still destructively fire against whatever's pending from an
// unrelated interaction in between. Anything starting with "/rewind"
// itself is exempt — re-listing, re-targeting, or confirming are all
// part of the same rewind flow, not something else interrupting it.
func (m Model) dispatchText(text string) (Model, tea.Cmd) {
	if m.pendingRewind != nil && !strings.HasPrefix(strings.TrimSpace(text), "/rewind") {
		m.pendingRewind = nil
		m.appendLine(dimStyle.Render("  (rewind confirmation cancelled)"))
	}
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	if strings.HasPrefix(text, "!") {
		return m.runBang(strings.TrimPrefix(text, "!"))
	}
	return m.submitText(text)
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
	expanded, truncatedRefs := expandFileReferences(m.workDir, text)
	m.messages = append(m.messages, agent.Message{Role: "user", Content: expanded})
	m.appendLine(userStyle.Render("you  ") + text)
	if len(truncatedRefs) > 0 {
		m.appendLine(dimStyle.Render(fmt.Sprintf("  (@%s truncated to fit — the file is very large)", strings.Join(truncatedRefs, ", @"))))
	}
	m.startBusy(stateWaitingModel)

	ctx := m.newTurnContext()
	return m, tea.Batch(
		startStream(ctx, m.client, m.messages),
		saveSession(m.workDir, m.sessionID, m.messages),
		checkpointCmd(m.checkpointStore, firstLine(text), messageIndex),
	)
}

// dequeueOrSubmit delivers the next message queued while chisel was
// busy (see the stateWaitingModel/stateExecutingTool case in handleKey),
// if any — call after any transition back to stateInput, batched
// alongside whatever else that transition already needed to do (saving
// the session, notifying), so a message typed while busy isn't silently
// swallowed once the wait is finally over. Routed through dispatchText,
// not sent straight to submitText — a queued "/status" or "!git stash"
// needs to run as a command/bang, not be delivered to the model as a
// literal user message. Returns nil if nothing is queued.
func (m *Model) dequeueOrSubmit() tea.Cmd {
	if len(m.queuedMessages) == 0 {
		return nil
	}
	next := m.queuedMessages[0]
	m.queuedMessages = m.queuedMessages[1:]
	updated, cmd := m.dispatchText(next)
	*m = updated
	return cmd
}

// mergeBufferedBackgroundResults folds any background-task completions
// that arrived while a turn was still in flight into m.messages, now
// that the turn is fully resolved — see handleBackgroundTaskDone, which
// defers exactly this (but still shows the transcript line and notifies
// immediately) to avoid a synthetic message landing between an
// assistant's tool_calls and their own results, a shape the API rejects,
// if the completion's timing happened to land there. Call at every
// chokepoint that already means "the turn just settled" — in practice,
// every call site of dequeueOrSubmit.
func (m *Model) mergeBufferedBackgroundResults() {
	if len(m.pendingBackgroundResults) == 0 {
		return
	}
	m.messages = append(m.messages, m.pendingBackgroundResults...)
	m.pendingBackgroundResults = nil
}

// flushPendingBackgroundResults is mergeBufferedBackgroundResults plus a
// save Cmd, for callers that don't already have their own save in
// flight to piggyback the merge onto — nil if nothing was buffered.
func (m *Model) flushPendingBackgroundResults() tea.Cmd {
	if len(m.pendingBackgroundResults) == 0 {
		return nil
	}
	m.mergeBufferedBackgroundResults()
	return saveSession(m.workDir, m.sessionID, m.messages)
}

// turnSettledCmd bundles flushPendingBackgroundResults with
// dequeueOrSubmit for callers that just returned to stateInput and need
// nothing else special done — handleModelCheckResult and
// handleCompactResult both used to return to stateInput directly
// without either, so a message typed during a /model check or a
// /compact (including an *auto*-triggered one, reachable without the
// user ever typing /compact themselves) got queued and then never
// delivered, stranded until some unrelated later turn happened to
// complete. Callers with extra logic around the queued-or-not outcome
// (handleStreamComplete's "skip the notification if something was
// queued", handleStreamEvent's cancellation branch) call both directly
// instead, since they need to inspect dequeueOrSubmit's return value.
func (m *Model) turnSettledCmd() tea.Cmd {
	flush := m.flushPendingBackgroundResults()
	return tea.Batch(flush, m.dequeueOrSubmit())
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
		errText := interruptibleErrorText(ev.Err)
		// No retry hint for an interruption — esc means the user chose
		// to stop it, not that anything failed; the hint is for the
		// genuine-error case, where a user unfamiliar with /retry would
		// otherwise have no indication it exists.
		if !errors.Is(ev.Err, context.Canceled) {
			errText += " — try /retry"
		}
		m.appendLine(errorStyle.Render("error  " + errText))
		m.state = stateInput
		flush := m.flushPendingBackgroundResults()
		if queued := m.dequeueOrSubmit(); queued != nil {
			return m, tea.Batch(flush, queued)
		}
		// No notification for a cancellation — esc means the user is
		// already at the keyboard, mid-keypress; a genuine error while
		// they may have stepped away is the case worth surfacing.
		if errors.Is(ev.Err, context.Canceled) {
			return m, flush
		}
		return m, tea.Batch(flush, notifyIdle("chisel hit an error"))
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
	m.requestCount++
	m.lastContextTokens = usage.InputTokens
	if finishReason == "length" {
		m.appendLine(dimStyle.Render("(response truncated — hit the model's length limit)"))
	}
	m.pendingUses = resp.ToolCalls

	if len(m.pendingUses) == 0 {
		m.endTurn()
		m.state = stateInput
		m.mergeBufferedBackgroundResults()
		save := saveSession(m.workDir, m.sessionID, m.messages)

		// A queued message delivered here means chisel is about to be
		// busy again right away — skip the "chisel is done" notification
		// in that case, the user's already at the keyboard (that's how
		// the message got queued in the first place). Same reasoning
		// applies to auto-compact triggering right below: it's its own
		// turn starting immediately, not chisel actually going idle, so
		// the notification would be just as misleading there.
		queued := m.dequeueOrSubmit()
		autoCompacting := queued == nil && m.lastContextTokens >= contextWarnThreshold
		var notify tea.Cmd
		if queued == nil && !autoCompacting {
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
		gitStatus := refreshGitStatus(m.workDir)

		if autoCompacting {
			m.appendLine(dimStyle.Render("context is large — compacting automatically…"))
			m.startBusy(stateWaitingModel)
			return m, tea.Batch(save, notify, autoCommitCmd, gitStatus, compact(m.newTurnContext(), m.client, m.messages))
		}

		return m, tea.Batch(save, notify, queued, autoCommitCmd, gitStatus)
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
		flush := m.flushPendingBackgroundResults()
		return m, tea.Batch(flush, m.dequeueOrSubmit())
	}
	call := m.pendingUses[0]

	key := toolCallKey(call)
	if key == m.lastToolCallKey {
		m.toolCallRepeatCount++
	} else {
		m.lastToolCallKey = key
		m.toolCallRepeatCount = 1
	}
	looping := m.toolCallRepeatCount >= doomLoopThreshold

	decision, reason := decidePermission(call, m.client.PlanMode(), m.sessionAllowlist, m.permRules)
	// A call that would otherwise run without asking (auto-allowed by
	// default, or already on the "always allow" list) still gets
	// escalated to a confirmation once it's repeated identically this
	// many times in a row — see doomLoopThreshold. An already-denied
	// call (plan mode) has nothing to escalate; it's blocked either way.
	if decision == permissionAllow && looping {
		decision = permissionAsk
	}

	switch decision {
	case permissionDeny:
		return m.handleToolResult(agent.ToolResult{ID: call.ID, Content: reason, IsError: true}, false)

	case permissionAsk:
		m.endTurn() // nothing async is in flight while waiting on a y/n decision
		m.state = stateAwaitingPermission
		m.awaitingLoopConfirmation = looping

		optionKeys := "y/n"
		if _, allowlistable := allowlistKey(call); allowlistable && !looping {
			optionKeys += "/a"
		}
		if _, _, persistable := persistableRuleFor(call); persistable && !looping {
			optionKeys += "/P"
		}
		options := "[" + optionKeys + "]"
		m.permissionHint = optionKeys + " · esc to deny"

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
		if looping {
			prompt = fmt.Sprintf("%s has been called %d times in a row with identical arguments — this may be stuck in a loop.\n\n%s",
				call.Function.Name, m.toolCallRepeatCount, prompt)
		}
		style := permissionStyle
		if m.width > 0 {
			style = style.Width(m.width)
		}
		m.appendPermissionLine(style.Render(prompt))
		return m, notifyIdle("chisel needs your permission")

	default: // permissionAllow
		m.startBusy(stateExecutingTool)
		m.appendLine(toolStyle.Render("  " + summarizeCall(call)))
		return m, executeTool(m.newTurnContext(), m.workDir, m.client.ModelName(), m.bash, m.mcp, m.hooks, m.skills, call)
	}
}

func (m Model) handleToolResult(result agent.ToolResult, interrupted bool) (tea.Model, tea.Cmd) {
	if len(m.pendingUses) == 0 {
		return m, nil
	}

	// Non-zero only for tools that make their own model requests under
	// the hood (dispatch_subagent) — without this, a subagent's real
	// cost, often the priciest single call in a turn since it's
	// multi-turn on its own, never showed up in the status bar's totals.
	m.tokensIn += result.Usage.InputTokens
	m.tokensOut += result.Usage.OutputTokens
	if result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0 {
		m.requestCount++ // a dispatch_subagent call — undercounts its own internal multi-turn requests, see requestCount's doc comment
	}

	if result.IsError {
		text := result.Content
		if interrupted {
			text = "interrupted"
		}
		m.appendToolResultEntry(interruptibleResultText(text), true)
	} else {
		m.appendToolResultEntry(result.Content, false)
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

	// esc cancelled this call's own context — but newTurnContext hands
	// dispatchNextTool a *fresh*, uncancelled context for whatever comes
	// next, so without this check a single esc only ever stopped the one
	// call in flight: the rest of pendingUses dispatched normally, and
	// once they all resolved chisel went right back to the model with
	// them, which typically just retried. Stop the whole turn instead —
	// resolve everything still queued with a synthetic result (the
	// history needs one per tool_calls entry regardless) and return to
	// stateInput rather than starting another model request.
	if interrupted {
		return m.abortTurnAfterInterruption()
	}

	if len(m.pendingUses) > 0 {
		return m.dispatchNextTool()
	}

	m.messages = append(m.messages, m.pendingResults...)
	m.pendingResults = nil
	m.startBusy(stateWaitingModel)
	ctx := m.newTurnContext()
	return m, tea.Batch(startStream(ctx, m.client, m.messages), saveSession(m.workDir, m.sessionID, m.messages))
}

// abortTurnAfterInterruption resolves every tool call still waiting in
// pendingUses (never dispatched — chisel processes these strictly
// sequentially) with a synthetic "interrupted" result, so the history
// stays API-valid (every tool_calls entry needs a matching result)
// without actually running any of them, then returns to stateInput
// without starting another model request. Called only from
// handleToolResult's interrupted branch, right after the call that was
// actually in flight already got its own real (cancelled) result
// appended to pendingResults.
func (m Model) abortTurnAfterInterruption() (tea.Model, tea.Cmd) {
	skipped := len(m.pendingUses)
	for _, call := range m.pendingUses {
		m.pendingResults = append(m.pendingResults, agent.ToolResult{
			ID: call.ID, Content: "Interrupted by user; this call was never run.", IsError: true,
		}.ToMessage())
	}
	m.pendingUses = nil
	if skipped > 0 {
		m.appendLine(dimStyle.Render(fmt.Sprintf("  (turn interrupted — %d more queued tool call(s) skipped)", skipped)))
	}

	m.messages = append(m.messages, m.pendingResults...)
	m.pendingResults = nil
	m.endTurn()
	m.state = stateInput

	flush := m.flushPendingBackgroundResults()
	save := saveSession(m.workDir, m.sessionID, m.messages)
	return m, tea.Batch(flush, save, m.dequeueOrSubmit())
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
