package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/skill"
)

// builtinCommandNames mirrors handleCommand's own switch — kept in sync
// with it the same way helpText already has to be — for tab completion
// (see commandNames/completeCommandInInput).
var builtinCommandNames = []string{
	"/help", "/model", "/think", "/new", "/compact", "/retry",
	"/git", "/plan", "/status", "/usage", "/rewind", "/queue",
	"/sessions", "/resume",
}

// commandNames returns every "/"-command name available for tab
// completion: the fixed built-ins above plus any user-defined custom
// commands loaded at startup (~/.chisel/commands, <workDir>/.chisel/commands).
func (m Model) commandNames() []string {
	names := append([]string{}, builtinCommandNames...)
	for _, name := range customcmd.Names(m.customCommands) {
		names = append(names, "/"+name)
	}
	sort.Strings(names)
	return names
}

// completeCommandInInput tab-completes a "/"-command name being typed —
// same longest-common-prefix behavior as @-file completion (fileref.go):
// a single match completes fully, plus a trailing space so the cursor
// lands ready for arguments; several matches complete only as far as
// they all agree and get listed in the transcript so an ambiguous
// completion isn't a silent no-op.
func (m *Model) completeCommandInInput() {
	value := strings.TrimRight(m.textArea.Value(), "\n")
	var matches []string
	for _, name := range m.commandNames() {
		if strings.HasPrefix(name, value) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return
	}
	if len(matches) == 1 {
		m.textArea.SetValue(matches[0] + " ")
		m.textArea.CursorEnd()
		return
	}
	if completed := longestCommonPrefix(matches); completed != value {
		m.textArea.SetValue(completed)
		m.textArea.CursorEnd()
	}
	m.appendLine(dimStyle.Render("  " + strings.Join(capCandidates(matches, maxCompletionCandidatesShown), "  ")))
}

// handleCommand processes a "/"-prefixed line from the input box instead of
// sending it to the model. Most commands are synchronous (nil Cmd); /model
// check and /retry are the exceptions — they make a real request, so they
// go through the same async Cmd/Msg path as a normal turn.
func (m Model) handleCommand(text string) (Model, tea.Cmd) {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/model":
		return m.handleModelCommand(fields[1:])
	case "/think":
		return m.handleThinkCommand(), nil
	case "/new":
		return m.handleNewCommand()
	case "/git":
		return m.handleGitCommand(fields[1:]), nil
	case "/compact":
		return m.handleCompactCommand()
	case "/plan":
		return m.handlePlanCommand(), nil
	case "/retry":
		return m.handleRetryCommand()
	case "/status":
		return m.handleStatusCommand(), nil
	case "/usage":
		return m.handleUsageCommand(), nil
	case "/rewind":
		return m.handleRewindCommand(fields[1:])
	case "/sessions":
		return m.handleSessionsCommand(), nil
	case "/resume":
		return m.handleResumeCommand(fields[1:])
	case "/queue":
		return m.handleQueueCommand(fields[1:]), nil
	case "/help":
		return m.handleHelpCommand(), nil
	default:
		return m.handleCustomOrUnknownCommand(text, fields)
	}
}

// handleCustomOrUnknownCommand checks fields[0] against the user-defined
// commands loaded at startup (~/.chisel/commands/*.md and
// <workDir>/.chisel/commands/*.md — see customcmd.Load) before falling
// back to reporting it as unknown. A match is expanded (substituting
// $ARGUMENTS with whatever followed the command name, or appending it if
// the template doesn't reference $ARGUMENTS at all) and submitted exactly
// like a normal typed message — no special trust gate, unlike hooks:
// this is canned prompt text, not code that runs automatically, so
// whatever the model does in response still goes through the normal
// permission gate.
func (m Model) handleCustomOrUnknownCommand(text string, fields []string) (Model, tea.Cmd) {
	name := strings.TrimPrefix(fields[0], "/")
	cmd, ok := m.customCommands[name]
	if !ok {
		m.appendLine(errorStyle.Render("unknown command: " + fields[0] + " — /help lists what's available"))
		return m, nil
	}

	args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	return m.submitText(customcmd.Expand(cmd, args))
}

// helpText is shown by /help and mirrors what handleCommand actually
// dispatches — keep the two in sync when adding or changing a command.
const helpText = `commands:
  /help                 show this list
  /model [name]         show available models, or switch to name
  /model check [name]   test a model through chisel's real request shape
  /think                toggle showing <think> blocks in full
  /new                  start a fresh session (previous one stays saved — /sessions, /resume)
  /compact              summarize the conversation to save context
  /retry                re-send the last request after a failure
  /git auto [on|off]    toggle auto-commit after each turn
  /plan                 toggle plan mode (read-only exploration only)
  /status               show workdir, session, hooks, MCP, and memory info
  /usage                show token/request counts since launch
  /rewind [n]           list checkpoints, or restore code+conversation to checkpoint n
  /sessions             list every saved session for this directory
  /resume [n]           list sessions, or switch this conversation to session n
  /queue [clear]        list messages queued while busy, or drop them all

keys:
  enter                 submit · in a permission prompt, only y/Y approves · while busy, queues instead
  alt+enter             insert a newline while composing a message
  @path                 reference a file — its content is sent to the model, tab to complete
  /command              tab-completes too — ambiguous prefixes list candidates
  !command              run a shell command directly, bypassing the model entirely — no permission prompt
  up / down             recall previous input (single-line only, persists across restarts)
  ctrl+r                incremental reverse search through input history
  ctrl+o                expand/collapse the most recent tool result
  ctrl+x                compose the current message in $EDITOR (falls back to vi)
  esc                   interrupt a running request/tool · deny a permission prompt
  y / n / a / p         approve / deny / always-allow-this-session / always-allow-permanently (bash only) in a permission prompt
  pgup/pgdn             scroll the transcript
  ctrl+u/d              half-page scroll the transcript (outside the input box — ctrl+u/d edit text while composing)
  mouse wheel           scroll the transcript · click+drag selects text and copies it on release
  ctrl+c                quit`

func (m Model) handleHelpCommand() Model {
	text := helpText
	if names := customcmd.Names(m.customCommands); len(names) > 0 {
		text += "\n\ncustom commands:\n  /" + strings.Join(names, ", /")
	}
	m.appendLine(dimStyle.Render(text))
	return m
}

// handleRetryCommand re-sends the current message history — recovery
// from a transient failure (a provider's 5xx, a dropped connection)
// without needing to retype whatever request triggered it. No new user
// message is appended; this is exactly what a normal turn's model
// request does, just without adding anything new to send first.
func (m Model) handleRetryCommand() (Model, tea.Cmd) {
	if len(m.messages) == 0 {
		m.appendLine(dimStyle.Render("nothing to retry yet"))
		return m, nil
	}
	m.appendLine(dimStyle.Render("retrying…"))
	m.startBusy(stateWaitingModel)
	ctx := m.newTurnContext()
	return m, startStream(ctx, m.client, m.messages)
}

// handleStatusCommand reports what chisel currently has loaded —
// almost all of which (hooks, MCP servers, memory files) is otherwise
// only ever shown once, at startup, and then never again for the rest
// of the session.
func (m Model) handleStatusCommand() Model {
	m.appendLine(dimStyle.Render("workdir: " + m.workDir))

	// The raw session ID is a timestamp, not something a human recognizes
	// at a glance — show the same friendly title /sessions already
	// derives for it (deriveTitle, via session.List) instead.
	title := m.sessionID
	if metas, err := session.List(m.workDir); err == nil {
		for _, meta := range metas {
			if meta.ID == m.sessionID {
				title = meta.Title
				break
			}
		}
	}
	m.appendLine(dimStyle.Render(fmt.Sprintf("session: %q (%d messages) — /sessions lists every saved session for this directory", title, len(m.messages))))

	if m.hooks.HasAny() {
		m.appendLine(dimStyle.Render(fmt.Sprintf("hooks: %d preToolUse, %d postToolUse", len(m.hooks.Hooks.PreToolUse), len(m.hooks.Hooks.PostToolUse))))
	} else {
		m.appendLine(dimStyle.Render("hooks: none configured"))
	}

	if m.permRules.HasAny() {
		ruleCount := 0
		for _, rules := range m.permRules {
			ruleCount += len(rules)
		}
		m.appendLine(dimStyle.Render(fmt.Sprintf("permission rules: %d rule(s) across %d tool(s)", ruleCount, len(m.permRules))))
	} else {
		m.appendLine(dimStyle.Render("permission rules: none configured"))
	}

	if statuses := m.mcp.Statuses(); len(statuses) == 0 {
		m.appendLine(dimStyle.Render("mcp: no servers configured"))
	} else {
		for _, s := range statuses {
			state := "ok"
			if s.Broken {
				state = "broken"
			}
			m.appendLine(dimStyle.Render(fmt.Sprintf("mcp: %s (%s)", s.Name, state)))
		}
	}

	if m.memUser || m.memProject {
		m.appendLine(dimStyle.Render("memory: " + memoryBannerText(m.memUser, m.memProject)))
	} else {
		m.appendLine(dimStyle.Render("memory: none loaded"))
	}

	if names := skill.Names(m.skills); len(names) > 0 {
		m.appendLine(dimStyle.Render("skills: " + strings.Join(names, ", ")))
	} else {
		m.appendLine(dimStyle.Render("skills: none loaded"))
	}

	return m
}

// handleUsageCommand reports cumulative token and request counts since
// chisel launched — deliberately not a dollar estimate against OpenCode
// Go's hard usage caps ($12/5hr, $30/week, $60/month; see docs/design.md
// §4), even though that would be the more directly useful number.
// Verified directly against the live API before building this: every
// response's own "cost" field (a trailing SSE frame after [DONE]) reads
// "0" regardless of request size, and there's no separate account/usage
// endpoint either — this subscription just doesn't expose real dollar
// cost data to estimate spend from. Claiming a specific dollar figure
// with no reliable source for it would be worse than not claiming one
// at all, the same reasoning chisel already applies to not maintaining
// a per-model context-window table.
//
// Labeled "since launch," not "session": these counters are process-
// lifetime and were never reset or reloaded across /new or /resume —
// fine when process and session were the same thing, but /sessions and
// /resume mean one process can now span several distinct saved
// conversations, and "session usage" would misleadingly suggest this
// only covers whichever one is currently active.
func (m Model) handleUsageCommand() Model {
	var b strings.Builder
	b.WriteString("usage since launch:\n")
	fmt.Fprintf(&b, "  requests:    %d\n", m.requestCount)
	fmt.Fprintf(&b, "  tokens in:   %s\n", formatTokenCount(m.tokensIn))
	fmt.Fprintf(&b, "  tokens out:  %s\n", formatTokenCount(m.tokensOut))
	fmt.Fprintf(&b, "  context now: %s tok\n\n", formatTokenCount(m.lastContextTokens))
	b.WriteString("OpenCode Go enforces hard usage caps ($12/5hr, $30/week, $60/month) but its API doesn't expose real dollar cost for this subscription — every response's own cost field reads \"0\" regardless of size, verified directly. Check opencode.ai's own dashboard for actual remaining budget.")

	m.appendLine(dimStyle.Render(b.String()))
	return m
}

// handleThinkCommand toggles whether inline <think> blocks render in
// full. Every assistant entry re-renders against the new setting the
// next time the viewport refreshes (appendLine, below, does that) —
// including ones from earlier in the conversation or a resumed session,
// since entry.render always applies the *current* showThinking rather
// than whatever was in effect when the entry was appended.
func (m Model) handleThinkCommand() Model {
	m.showThinking = !m.showThinking
	if m.showThinking {
		m.appendLine(dimStyle.Render("thinking blocks: shown"))
	} else {
		m.appendLine(dimStyle.Render("thinking blocks: hidden"))
	}
	return m
}

// handlePlanCommand toggles plan mode: the model is told (via the system
// prompt) to only explore and present a plan, and any mutating tool call
// it makes anyway is hard-denied at dispatch time regardless — see
// dispatchNextTool in update.go. Off by default.
func (m Model) handlePlanCommand() Model {
	m.client.SetPlanMode(!m.client.PlanMode())
	if m.client.PlanMode() {
		m.appendLine(dimStyle.Render("plan mode: on — read-only exploration only; /plan again to exit and allow changes"))
	} else {
		m.appendLine(dimStyle.Render("plan mode: off"))
	}
	return m
}

// handleNewCommand abandons the current transcript in memory and starts
// a fresh session — a new session.NewID, not a deletion: the old
// session's file is untouched on disk and still resumable via /sessions
// + /resume. Doesn't touch which model is selected or the bash session's
// cd/env state — those aren't part of "the conversation". Also clears
// todos and checkpoints: both are tied to the conversation just
// abandoned — a checkpoint's messageIndex in particular refers to a
// position in the now-gone m.messages, and leaving it behind meant a
// later /rewind could slice m.messages past its new, shorter length and
// panic. Same reasoning for lastToolResultIdx (ctrl+o could otherwise
// expand an entry from the old conversation) and the doom-loop counters
// (a repeat count from the abandoned conversation shouldn't carry into
// this one). Saves immediately, with zero messages, rather than waiting
// for the next turn to complete — otherwise quitting right after /new
// resumed the *old* session next launch, since nothing durable recorded
// that a new one had started (session.List/LoadByID treat a zero-message
// session as real specifically so this save means something).
func (m Model) handleNewCommand() (Model, tea.Cmd) {
	m.sessionID = session.NewID()
	m.messages = nil
	m.entries = nil
	m.lastContextTokens = 0
	m.todos = nil
	m.checkpoints = nil
	m.pendingRewind = nil
	m.lastToolResultIdx = -1
	m.lastToolCallKey = ""
	m.toolCallRepeatCount = 0
	m.viewport.SetContent("")
	m.recomputeViewportHeight()
	m.appendLine(dimStyle.Render("started a new session — the previous one is still saved; /sessions lists it, /resume <n> switches back"))
	return m, saveSession(m.workDir, m.sessionID, m.messages)
}

// handleQueueCommand implements /queue [clear] — messages typed while
// chisel was busy (see the stateWaitingModel/stateExecutingTool case in
// handleKey) previously only showed up as a bare count in the status
// bar, with no way to see what they actually said or to change your mind
// about sending one.
func (m Model) handleQueueCommand(args []string) Model {
	if len(args) == 0 {
		if len(m.queuedMessages) == 0 {
			m.appendLine(dimStyle.Render("queue is empty"))
			return m
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d message(s) queued:\n", len(m.queuedMessages))
		for i, msg := range m.queuedMessages {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, firstLine(msg))
		}
		b.WriteString("type /queue clear to drop them")
		m.appendLine(dimStyle.Render(b.String()))
		return m
	}

	if args[0] != "clear" {
		m.appendLine(dimStyle.Render("usage: /queue [clear]"))
		return m
	}
	n := len(m.queuedMessages)
	m.queuedMessages = nil
	m.appendLine(dimStyle.Render(fmt.Sprintf("cleared %d queued message(s)", n)))
	return m
}

// handleGitCommand toggles auto-commit: off by default, since silently
// creating git history is exactly the kind of thing that should be opted
// into, not assumed. Refuses to turn on outside a git repo rather than
// leaving a setting that would just silently no-op every turn.
func (m Model) handleGitCommand(args []string) Model {
	if len(args) == 0 || args[0] != "auto" {
		m.appendLine(dimStyle.Render("usage: /git auto [on|off]"))
		return m
	}

	if len(args) == 1 {
		state := "off"
		if m.autoCommit {
			state = "on"
		}
		m.appendLine(dimStyle.Render("auto-commit: " + state))
		return m
	}

	switch args[1] {
	case "on":
		if !gitutil.IsRepo(m.workDir) {
			m.appendLine(errorStyle.Render(m.workDir + " isn't a git repository — run git init first"))
			return m
		}
		m.autoCommit = true
		m.appendLine(dimStyle.Render("auto-commit: on — chisel will commit its own changes after each turn"))
	case "off":
		m.autoCommit = false
		m.appendLine(dimStyle.Render("auto-commit: off"))
	default:
		m.appendLine(errorStyle.Render("usage: /git auto [on|off]"))
	}
	return m
}

func (m Model) handleModelCommand(args []string) (Model, tea.Cmd) {
	if len(args) == 0 {
		current := m.client.ModelName()
		m.appendLine(dimStyle.Render("available models (current marked ›):"))
		for _, name := range agent.KnownModels() {
			marker := "    "
			if name == current {
				marker = "  › "
			}
			m.appendLine(dimStyle.Render(marker + name))
		}
		m.appendLine(dimStyle.Render("  usage: /model <name>  ·  /model check [name]"))
		return m, nil
	}

	if args[0] == "check" {
		target := m.client.ModelName()
		if len(args) > 1 {
			target = args[1]
		}
		m.appendLine(dimStyle.Render("checking " + target + "…"))
		m.startBusy(stateWaitingModel)
		return m, checkModel(m.newTurnContext(), m.client, target)
	}

	name := args[0]
	m.client.SetModel(name)
	m.appendLine(dimStyle.Render("switched to " + name))
	return m, nil
}

// checkModel sends a minimal request to name through chisel's real request
// shape — same system prompt, tool declarations (including any MCP
// tools), and memory content the live client is actually using, via
// client.Clone — so it catches the same class of failure found earlier
// in this project's development (a model or provider rejecting the
// tool set outright), not just plain reachability. Cloning matters once
// MCP servers are configured: a bare client with none of that has a
// different (smaller) request shape than what a real turn sends.
func checkModel(ctx context.Context, client *agent.Client, name string) tea.Cmd {
	return func() tea.Msg {
		probe := client.Clone(name)
		ch, err := probe.SendStreaming(ctx, []agent.Message{
			{Role: "user", Content: "Reply with exactly the word: ok"},
		})
		if err != nil {
			return modelCheckResultMsg{model: name, err: err}
		}

		msg, usage, err := agent.Drain(ch)
		if err != nil {
			return modelCheckResultMsg{model: name, err: err}
		}
		return modelCheckResultMsg{model: name, reply: msg.Content, usage: usage}
	}
}

func (m Model) handleModelCheckResult(msg modelCheckResultMsg) (tea.Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	// A probe sends the full request shape (system prompt + entire tool
	// set) through client.Clone, not a trivially small request — its
	// real cost belongs in the running totals the same way
	// handleCompactResult already counts its own equally-synthetic
	// request, unconditionally: a request was made either way, and
	// Drain never returns partial usage on error, so tokensIn/Out just
	// add zero in that case.
	m.tokensIn += msg.usage.InputTokens
	m.tokensOut += msg.usage.OutputTokens
	m.requestCount++
	if msg.err != nil {
		m.appendLine(errorStyle.Render(fmt.Sprintf("✗ %s: %s", msg.model, interruptibleErrorText(msg.err))))
		return m, m.turnSettledCmd()
	}
	m.appendLine(dimStyle.Render(fmt.Sprintf("✓ %s: %s", msg.model, firstLine(msg.reply))))
	return m, m.turnSettledCmd()
}

// handleCompactCommand asks the model to summarize the conversation so
// far and replaces the history with that summary — chisel's answer to
// "manual context management" since there's no server-side compaction to
// lean on.
func (m Model) handleCompactCommand() (Model, tea.Cmd) {
	if len(m.messages) == 0 {
		m.appendLine(dimStyle.Render("nothing to compact yet"))
		return m, nil
	}
	m.appendLine(dimStyle.Render("compacting…"))
	m.startBusy(stateWaitingModel)
	return m, compact(m.newTurnContext(), m.client, m.messages)
}

func (m Model) handleCompactResult(msg compactResultMsg) (tea.Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	m.tokensIn += msg.usage.InputTokens
	m.tokensOut += msg.usage.OutputTokens
	m.requestCount++

	if msg.err != nil {
		m.appendLine(errorStyle.Render("compact failed: " + interruptibleErrorText(msg.err)))
		return m, m.turnSettledCmd()
	}

	m.messages = compactedHistory(msg.summary)
	m.entries = nil
	m.lastContextTokens = 0
	// Every checkpoint's messageIndex refers to a position in the
	// conversation just replaced with a single summary message — same
	// stale-index hazard /new guards against, see its own doc comment.
	m.checkpoints = nil
	m.pendingRewind = nil
	m.viewport.SetContent("")
	m.appendLine(dimStyle.Render("── conversation compacted ──"))
	m.appendAssistantEntry(msg.summary)

	save := saveSession(m.workDir, m.sessionID, m.messages)
	return m, tea.Batch(save, m.turnSettledCmd())
}
