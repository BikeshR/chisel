package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/agentmemory"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/skill"
)

// builtinCommandNames mirrors handleCommand's own switch — kept in sync
// with it the same way helpText already has to be — for the live
// command-palette dropdown (see commandNames, refreshCommandPalette in
// picker.go).
var builtinCommandNames = []string{
	"/help", "/model", "/think", "/new", "/compact", "/retry",
	"/git", "/plan", "/accept-edits", "/status", "/usage", "/rewind", "/queue",
	"/sessions", "/resume", "/memory", "/branch", "/goal", "/context", "/diff", "/hooks",
	"/mcp-prompts", "/mcp-prompt", "/mcp-resources", "/mcp-resource", "/tasks", "/export",
}

// commandNames returns every "/"-command name available for the
// command palette: the fixed built-ins above plus any user-defined
// custom commands loaded at startup (~/.chisel/commands,
// <workDir>/.chisel/commands).
func (m Model) commandNames() []string {
	names := append([]string{}, builtinCommandNames...)
	for _, name := range customcmd.Names(m.customCommands) {
		names = append(names, "/"+name)
	}
	sort.Strings(names)
	return names
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
	case "/branch":
		return m.handleBranchCommand()
	case "/git":
		return m.handleGitCommand(fields[1:]), nil
	case "/compact":
		return m.handleCompactCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/plan":
		return m.handlePlanCommand(), nil
	case "/accept-edits":
		return m.handleAcceptEditsCommand(), nil
	case "/retry":
		return m.handleRetryCommand()
	case "/status":
		return m.handleStatusCommand(), nil
	case "/hooks":
		return m.handleHooksCommand(fields[1:]), nil
	case "/mcp-prompts":
		return m.handleMCPPromptsCommand(), nil
	case "/mcp-prompt":
		return m.handleMCPPromptCommand(fields[1:])
	case "/mcp-resources":
		return m.handleMCPResourcesCommand(), nil
	case "/mcp-resource":
		return m.handleMCPResourceCommand(fields[1:])
	case "/usage":
		return m.handleUsageCommand(), nil
	case "/context":
		return m.handleContextCommand(), nil
	case "/rewind":
		return m.handleRewindCommand(fields[1:])
	case "/diff":
		return m.handleDiffCommand(), nil
	case "/sessions":
		return m.handleSessionsCommand(), nil
	case "/resume":
		return m.handleResumeCommand(fields[1:])
	case "/queue":
		return m.handleQueueCommand(fields[1:]), nil
	case "/tasks":
		return m.handleTasksCommand(fields[1:]), nil
	case "/export":
		return m.handleExportCommand(fields[1:]), nil
	case "/memory":
		return m.handleMemoryCommand(fields[1:]), nil
	case "/goal":
		return m.handleGoalCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0]))), nil
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
	return m.submitTextWithHookCheck(customcmd.Expand(cmd, args))
}

// helpText is shown by /help and mirrors what handleCommand actually
// dispatches — keep the two in sync when adding or changing a command.
const helpText = `commands:
  /help                 show this list
  /model [name]         switch to name, or open an interactive picker if no name is given
  /model check [name]   test a model through chisel's real request shape
  /model planner [name|clear]   show/set/clear a secondary model used for plan-mode turns
  /think                toggle showing <think> blocks in full
  /new                  start a fresh session (previous one stays saved — /sessions, /resume)
  /branch               fork this conversation into a new session, keeping the current one too
  /compact [focus]       summarize the conversation to save context, optionally steered toward focus
  /retry                re-send the last request after a failure
  /git auto [on|off]    toggle auto-commit after each turn
  /plan                 toggle plan mode (read-only exploration only)
  /accept-edits         toggle accept-edits mode (file edits run without asking; bash/MCP still ask)
  /status               show workdir, session, hooks, MCP, and memory info
  /hooks init            scaffold .chisel/hooks.json with a working lint-after-edit example
  /mcp-prompts           list MCP prompt templates available across running servers
  /mcp-prompt <server> <name> [k=v ...]   fetch and submit an MCP prompt template
  /mcp-resources         list MCP resources available across running servers
  /mcp-resource <server> <uri>            load an MCP resource into the conversation
  /usage                show token/request counts since launch
  /context              rough estimated breakdown of what's using context, by category
  /rewind [n]           list checkpoints, or restore code+conversation to checkpoint n
  /diff                 show cumulative file changes since the last checkpoint
  /sessions             list every saved session for this directory
  /resume [n]           list sessions, or switch this conversation to session n
  /queue [clear]        list messages queued while busy, or drop them all
  /tasks [cancel <id>]   list background tasks (bash_background), or cancel one
  /export [path]         write the conversation to a markdown file (default: ./chisel-export-<timestamp>.md)
  /memory [clear]       show what the model has remembered about this project, or clear it
  /goal [text|clear]    set a standing goal to auto-continue toward after each turn, show it, or clear it

keys:
  enter                 submit · in a permission prompt, only y/Y approves · while busy, queues instead
  alt+enter             insert a newline while composing a message
  @path                 reference a file — its content is sent to the model, tab to complete
  /command              shows a live dropdown of matching commands as you type — ↑/↓ to browse, tab fills it in, enter runs it
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

// charsPerTokenEstimate is a common rough heuristic for English text
// (~4 characters per token) — used only because chisel has no real
// tokenizer, not because it's precise. /context labels every number it
// produces from this as an estimate; the actual total for the most
// recent real request (server-reported, exact) is shown alongside it
// for calibration, not folded into the same estimated figures.
const charsPerTokenEstimate = 4

// handleContextCommand implements /context: a breakdown of what's
// actually taking up space in the request chisel would send right now,
// by category — not a context-*window* capacity meter (chisel
// deliberately doesn't maintain a per-model window-size table; see
// docs/design.md) and not a substitute for /usage's authoritative,
// server-reported totals. Useful for "what's actually eating my
// context" the same way a du -h breakdown is useful without needing to
// be byte-exact.
func (m Model) handleContextCommand() Model {
	b := m.client.PromptBreakdown()
	transcriptChars := 0
	for _, msg := range m.messages {
		data, _ := json.Marshal(msg)
		transcriptChars += len(data)
	}
	estimate := func(chars int) int64 { return int64(chars) / charsPerTokenEstimate }

	var buf strings.Builder
	buf.WriteString("context breakdown (rough estimate, ~4 chars/token — not an exact count):\n")
	fmt.Fprintf(&buf, "  base instructions   ~%s tok\n", formatTokenCount(estimate(b.BaseInstructions)))
	fmt.Fprintf(&buf, "  project memory      ~%s tok\n", formatTokenCount(estimate(b.ProjectMemory)))
	fmt.Fprintf(&buf, "  skills              ~%s tok\n", formatTokenCount(estimate(b.Skills)))
	fmt.Fprintf(&buf, "  tool schemas        ~%s tok\n", formatTokenCount(estimate(b.ToolSchemas)))
	fmt.Fprintf(&buf, "  transcript          ~%s tok\n\n", formatTokenCount(estimate(transcriptChars)))
	fmt.Fprintf(&buf, "actual total, last real request: %s tok (server-reported, exact)", formatTokenCount(m.lastContextTokens))

	m.appendLine(dimStyle.Render(buf.String()))
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
// dispatchNextTool in update.go. Off by default. Switching into plan
// mode always wins over accept-edits, same as it wins over everything
// else decidePermission checks — they're mutually exclusive states of
// the one underlying agent.Mode, not two independent flags.
func (m Model) handlePlanCommand() Model {
	if m.client.Mode() == agent.ModePlan {
		m.client.SetMode(agent.ModeNormal)
		m.appendLine(dimStyle.Render("plan mode: off"))
	} else {
		m.client.SetMode(agent.ModePlan)
		m.appendLine(dimStyle.Render("plan mode: on — read-only exploration only; /plan again to exit and allow changes"))
	}
	return m
}

// handleAcceptEditsCommand toggles accept-edits mode: file edits (only
// chisel's own str_replace_based_edit_tool, still diffed in the
// transcript, still confined to resolveInWorkDir) run without asking;
// bash and MCP calls are entirely unaffected and still ask every time,
// same as always — this is deliberately narrower than a general
// auto-approve mode, since chisel has no bash sandbox (see
// isEditCall/decidePermission in permission.go for why that
// distinction is load-bearing, not incidental). Off by default;
// switching into it always wins over plan mode, the same way switching
// into plan mode wins over this.
func (m Model) handleAcceptEditsCommand() Model {
	if m.client.Mode() == agent.ModeAcceptEdits {
		m.client.SetMode(agent.ModeNormal)
		m.appendLine(dimStyle.Render("accept-edits mode: off"))
	} else {
		m.client.SetMode(agent.ModeAcceptEdits)
		m.appendLine(dimStyle.Render("accept-edits mode: on — file edits run without asking; bash and MCP calls still ask every time; /accept-edits again to turn off"))
	}
	return m
}

// hooksInitScaffold is /hooks init's starting point — a real, working
// example (not just a comment, since JSON has none) of the lint-after-
// edit pattern: gofmt runs on whatever file a str_replace_based_edit_tool
// call just touched, and its output (if any) is folded back into what
// the model sees. Safe on a non-Go project too — gofmt errors on a
// non-.go path, and "|| true" swallows that rather than surfacing a
// confusing failure for a hook that's about to be edited anyway.
const hooksInitScaffold = `{
  "hooks": {
    "postToolUse": [
      {
        "match": "str_replace_based_edit_tool",
        "command": "gofmt -l \"$CHISEL_HOOK_PATH\" 2>/dev/null || true"
      }
    ]
  }
}
`

// handleHooksCommand implements /hooks init: chisel could already do
// the lint-after-edit pattern several mainstream tools ship as a named
// feature (Aider's --auto-lint) via a plain postToolUse hook — the gap
// was discoverability, not capability, so this just writes a working
// starting point rather than adding a new mechanism.
func (m Model) handleHooksCommand(args []string) Model {
	if len(args) == 0 || args[0] != "init" {
		m.appendLine(dimStyle.Render("usage: /hooks init — scaffold .chisel/hooks.json with a working lint-after-edit example"))
		return m
	}

	path := hooks.ConfigPath(m.workDir)
	if _, err := os.Stat(path); err == nil {
		m.appendLine(errorStyle.Render(".chisel/hooks.json already exists — not overwriting it"))
		return m
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.appendLine(errorStyle.Render("scaffolding hooks: " + err.Error()))
		return m
	}
	if err := os.WriteFile(path, []byte(hooksInitScaffold), 0o644); err != nil {
		m.appendLine(errorStyle.Render("scaffolding hooks: " + err.Error()))
		return m
	}
	m.appendLine(dimStyle.Render("wrote .chisel/hooks.json with a lint-after-edit example (postToolUse, gofmt) — edit the command for your project, then restart chisel to pick it up (you'll be asked to trust it)"))
	return m
}

// handleMemoryCommand shows or clears the project's agent-writable
// memory (.chisel/MEMORY.md, see internal/agentmemory and the remember
// tool) — distinct from CHISEL.md/AGENTS.md, which /status already
// covers: this is what the model itself has chosen to persist across
// sessions, not what the user wrote for it to read.
func (m Model) handleMemoryCommand(args []string) Model {
	if len(args) == 0 {
		content, found := agentmemory.Load(m.workDir)
		if !found {
			m.appendLine(dimStyle.Render("memory: nothing remembered yet in this project"))
			return m
		}
		m.appendLine(dimStyle.Render("remembered about this project:\n" + content + "\n\ntype /memory clear to forget all of it"))
		return m
	}

	if args[0] != "clear" {
		m.appendLine(dimStyle.Render("usage: /memory [clear]"))
		return m
	}
	if err := agentmemory.Clear(m.workDir); err != nil {
		m.appendLine(errorStyle.Render("failed to clear memory: " + err.Error()))
		return m
	}
	m.client.SetAgentMemory("")
	m.appendLine(dimStyle.Render("memory cleared"))
	return m
}

// maxGoalContinuations bounds how many times in a row chisel will
// auto-submit a "keep going" follow-up toward /goal's standing
// condition before stopping and waiting for the user — a runaway loop
// that never considers the goal satisfied would otherwise keep
// dispatching real turns (and their own tool calls, still permission-
// gated, but each one a real request) indefinitely. Generous, not
// tight: this is a backstop, not an expected ceiling for ordinary use.
const maxGoalContinuations = 25

// handleGoalCommand implements /goal [text|clear]. Setting or clearing
// a goal always resets goalContinuations and assistantTextRepeatCount —
// a fresh goal (or none at all) starts its own budget rather than
// inheriting whatever was left from a previous one.
func (m Model) handleGoalCommand(arg string) Model {
	if arg == "" {
		if m.goal == "" {
			m.appendLine(dimStyle.Render("no goal set — /goal <text> sets one"))
			return m
		}
		m.appendLine(dimStyle.Render("current goal: " + m.goal))
		return m
	}
	if arg == "clear" {
		m.goal = ""
		m.goalContinuations = 0
		m.assistantTextRepeatCount = 0
		m.appendLine(dimStyle.Render("goal cleared"))
		return m
	}
	m.goal = arg
	m.goalContinuations = 0
	m.assistantTextRepeatCount = 0
	m.appendLine(dimStyle.Render("goal set: " + m.goal + " — chisel will keep going toward it after each turn instead of stopping; /goal clear to stop"))
	return m
}

// assistantTextRepeatThreshold reuses doomLoopThreshold's own number —
// opencode's 2026 extension of the identical-tool-call guard to also
// catch repeated reasoning/output applies here: a model whose final
// text response comes back verbatim identical this many turns in a row
// is stuck, not making progress, the same failure mode doomLoopThreshold
// already catches for repeated tool calls. Checked only by
// continueTowardGoal today — a normal (non-goal) turn ending in
// repeated text just returns control to the user anyway, so there's
// nothing automatic to guard against there.
const assistantTextRepeatThreshold = doomLoopThreshold

// continueTowardGoal auto-submits a "keep going" follow-up toward the
// standing /goal, the same path a real typed message takes
// (submitText) — every tool call it leads to still goes through the
// normal permission gate exactly like any other turn; this only
// automates the re-prompting between turns. Returns a nil Cmd once
// maxGoalContinuations is hit, or once the model's own responses start
// repeating verbatim (assistantTextRepeatThreshold) — continuing
// against a model that's already stuck saying the same thing would
// just burn through the continuation budget for no new progress — so
// the caller falls back to its normal idle handling instead of
// continuing indefinitely either way.
func (m Model) continueTowardGoal() (Model, tea.Cmd) {
	if m.goalContinuations >= maxGoalContinuations {
		m.appendLine(dimStyle.Render(fmt.Sprintf("goal continuation limit reached (%d) — /goal to check it, /goal clear to stop, or just tell me to keep going", maxGoalContinuations)))
		m.goal = ""
		return m, nil
	}
	if m.assistantTextRepeatCount >= assistantTextRepeatThreshold {
		m.appendLine(dimStyle.Render(fmt.Sprintf("stopped auto-continuing — the last %d responses were identical, which usually means the goal is stuck rather than still being worked on. /goal to check it, /goal clear to stop, or tell me what to do differently.", m.assistantTextRepeatCount)))
		m.goal = ""
		return m, nil
	}
	m.goalContinuations++
	return m.submitTextWithHookCheck("Continue working toward this goal: " + m.goal)
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

// handleBranchCommand forks the current conversation into a new,
// independently resumable session — unlike /new (abandons the current
// thread for a blank one) or /resume (switches to an already-saved
// one), /branch lets you try a different approach without losing
// either: the original session's file is untouched (chisel already
// saves after every turn, so it's already durable as of right before
// this), and this live conversation continues from here under a fresh
// session id. Deliberately doesn't reset messages/entries/todos/
// checkpoints the way /new does — that's the entire point, the fork
// starts as an exact copy of what's already here, not a blank slate.
func (m Model) handleBranchCommand() (Model, tea.Cmd) {
	m.sessionID = session.NewID()
	m.appendLine(dimStyle.Render("branched into a new session — the original is still saved under its own id; /sessions lists it, /resume switches back"))
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
		// An interactive picker instead of a static list the user then
		// had to retype a name from — see modelPickerActive,
		// handleModelPickerKey (picker.go). Pre-selects the current
		// model rather than always starting at the top of the list.
		m.modelPickerActive = true
		m.modelPickerSelected = 0
		for i, name := range agent.KnownModels() {
			if name == m.client.ModelName() {
				m.modelPickerSelected = i
				break
			}
		}
		m.recomputeViewportHeight()
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

	if args[0] == "planner" {
		return m.handleModelPlannerCommand(args[1:]), nil
	}

	name := args[0]
	m.client.SetModel(name)
	m.appendLine(dimStyle.Render("switched to " + name))
	return m, nil
}

// handleModelPlannerCommand implements /model planner [name|clear] — a
// secondary model used instead of the primary one for requests sent
// while in plan mode (see agent.Client.EffectiveModelName), mirroring
// Goose's GOOSE_PLANNER_MODEL and Aider's architect/editor split: a
// cheaper/faster model can handle exploration-and-propose turns while
// the primary model does the real work, using two model IDs from the
// one provider chisel already talks to, not a second provider. Also
// settable at startup via CHISEL_PLANNER_MODEL — this changes it for
// the rest of the current session, same as /model does for the primary.
func (m Model) handleModelPlannerCommand(args []string) Model {
	if len(args) == 0 {
		if p := m.client.PlannerModel(); p != "" {
			m.appendLine(dimStyle.Render(fmt.Sprintf("planner model: %s (used instead of %s for plan-mode turns)", p, m.client.ModelName())))
		} else {
			m.appendLine(dimStyle.Render(fmt.Sprintf("no planner model set — plan mode uses %s, same as everything else; /model planner <name> sets one", m.client.ModelName())))
		}
		return m
	}
	if args[0] == "clear" {
		m.client.SetPlannerModel("")
		m.appendLine(dimStyle.Render("planner model cleared — plan mode will use " + m.client.ModelName()))
		return m
	}
	m.client.SetPlannerModel(args[0])
	m.appendLine(dimStyle.Render(fmt.Sprintf("planner model set: %s — used instead of %s for plan-mode turns", args[0], m.client.ModelName())))
	return m
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
// lean on. focus is an optional instruction (Cursor's /summarize and
// Claude Code's /compact <instructions> both take one) folded into the
// compaction prompt so the summary can be steered toward what actually
// matters for what comes next, rather than always the same generic shape.
func (m Model) handleCompactCommand(focus string) (Model, tea.Cmd) {
	if len(m.messages) == 0 {
		m.appendLine(dimStyle.Render("nothing to compact yet"))
		return m, nil
	}
	notice := "compacting…"
	if focus != "" {
		notice = fmt.Sprintf("compacting (focus: %s)…", focus)
	}
	m.appendLine(dimStyle.Render(notice))
	m.startBusy(stateWaitingModel)
	return m, compact(m.newTurnContext(), m.client, m.messages, focus, m.workDir, m.hooks)
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
