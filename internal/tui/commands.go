package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/session"
)

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
		return m.handleNewCommand(), nil
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
	case "/rewind":
		return m.handleRewindCommand(fields[1:]), nil
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
  /new                  start a fresh session (clears saved history)
  /compact              summarize the conversation to save context
  /retry                re-send the last request after a failure
  /git auto [on|off]    toggle auto-commit after each turn
  /plan                 toggle plan mode (read-only exploration only)
  /status               show workdir, session, hooks, MCP, and memory info
  /rewind [n]           list checkpoints, or restore code+conversation to checkpoint n

keys:
  enter                 submit · in a permission prompt, only y/Y approves · while busy, queues instead
  alt+enter             insert a newline while composing a message
  @path                 reference a file — its content is sent to the model, tab to complete
  esc                   interrupt a running request/tool · deny a permission prompt
  y / n / a             approve / deny / always-allow-this-session in a permission prompt
  pgup/pgdn, ctrl+u/d   scroll the transcript
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
	m.state = stateWaitingModel
	ctx := m.newTurnContext()
	return m, startStream(ctx, m.client, m.messages)
}

// handleStatusCommand reports what chisel currently has loaded —
// almost all of which (hooks, MCP servers, memory files) is otherwise
// only ever shown once, at startup, and then never again for the rest
// of the session.
func (m Model) handleStatusCommand() Model {
	m.appendLine(dimStyle.Render("workdir: " + m.workDir))

	if path, err := session.Path(m.workDir); err == nil {
		if info, err := os.Stat(path); err == nil {
			m.appendLine(dimStyle.Render(fmt.Sprintf("session: %s (%d messages, saved %s)", path, len(m.messages), humanizeSince(info.ModTime()))))
		} else {
			m.appendLine(dimStyle.Render("session: not yet saved"))
		}
	}

	if m.hooks.HasAny() {
		m.appendLine(dimStyle.Render(fmt.Sprintf("hooks: %d preToolUse, %d postToolUse", len(m.hooks.Hooks.PreToolUse), len(m.hooks.Hooks.PostToolUse))))
	} else {
		m.appendLine(dimStyle.Render("hooks: none configured"))
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

// handleNewCommand abandons the current transcript, in memory and on
// disk, and starts fresh. Doesn't touch which model is selected or the
// bash session's cd/env state — those aren't part of "the conversation".
// The clear error (if any) is appended *after* the reset, not before —
// appending first and then wiping m.entries would erase it before it was
// ever actually shown.
func (m Model) handleNewCommand() Model {
	clearErr := session.Clear(m.workDir)
	m.messages = nil
	m.entries = nil
	m.lastContextTokens = 0
	m.viewport.SetContent("")
	m.appendLine(dimStyle.Render("started a new session"))
	if clearErr != nil {
		m.appendLine(errorStyle.Render("failed to clear saved session: " + clearErr.Error()))
	}
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
		m.state = stateWaitingModel
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

		msg, _, err := agent.Drain(ch)
		if err != nil {
			return modelCheckResultMsg{model: name, err: err}
		}
		return modelCheckResultMsg{model: name, reply: msg.Content}
	}
}

func (m Model) handleModelCheckResult(msg modelCheckResultMsg) (tea.Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	if msg.err != nil {
		m.appendLine(errorStyle.Render(fmt.Sprintf("✗ %s: %s", msg.model, interruptibleErrorText(msg.err))))
		return m, nil
	}
	m.appendLine(dimStyle.Render(fmt.Sprintf("✓ %s: %s", msg.model, firstLine(msg.reply))))
	return m, nil
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
	m.state = stateWaitingModel
	return m, compact(m.newTurnContext(), m.client, m.messages)
}

func (m Model) handleCompactResult(msg compactResultMsg) (tea.Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	m.tokensIn += msg.usage.InputTokens
	m.tokensOut += msg.usage.OutputTokens

	if msg.err != nil {
		m.appendLine(errorStyle.Render("compact failed: " + interruptibleErrorText(msg.err)))
		return m, nil
	}

	m.messages = compactedHistory(msg.summary)
	m.entries = nil
	m.lastContextTokens = 0
	m.viewport.SetContent("")
	m.appendLine(dimStyle.Render("── conversation compacted ──"))
	m.appendAssistantEntry(msg.summary)

	return m, saveSession(m.workDir, m.messages)
}
