package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/session"
)

// handleCommand processes a "/"-prefixed line from the input box instead of
// sending it to the model. Most commands are synchronous (nil Cmd); /model
// check is the exception — it makes a real request, so it goes through the
// same async Cmd/Msg path as a normal turn.
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
	default:
		m.appendLine(errorStyle.Render("unknown command: " + fields[0]))
		return m, nil
	}
}

// handleThinkCommand toggles whether inline <think> blocks render in full.
// Only affects turns from here on — already-rendered lines keep whatever
// form they were collapsed (or not) to at the time.
func (m Model) handleThinkCommand() Model {
	m.showThinking = !m.showThinking
	if m.showThinking {
		m.appendLine(dimStyle.Render("thinking blocks: shown (for new messages)"))
	} else {
		m.appendLine(dimStyle.Render("thinking blocks: hidden (for new messages)"))
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
func (m Model) handleNewCommand() Model {
	if err := session.Clear(m.workDir); err != nil {
		m.appendLine(errorStyle.Render("failed to clear saved session: " + err.Error()))
	}
	m.messages = nil
	m.lines = nil
	m.viewport.SetContent("")
	m.appendLine(dimStyle.Render("started a new session"))
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
		return m, checkModel(target)
	}

	name := args[0]
	m.client = agent.New(name)
	m.appendLine(dimStyle.Render("switched to " + name))
	return m, nil
}

// checkModel sends a minimal request to name through chisel's real request
// shape — same system prompt and tool declarations a normal turn would use
// — so it catches the same class of failure found earlier in this
// project's development (a model or provider rejecting the tool set
// outright), not just plain reachability.
func checkModel(name string) tea.Cmd {
	return func() tea.Msg {
		client := agent.New(name)
		ch, err := client.SendStreaming(context.Background(), []agent.Message{
			{Role: "user", Content: "Reply with exactly the word: ok"},
		})
		if err != nil {
			return modelCheckResultMsg{model: name, err: err}
		}

		var final agent.Event
		for ev := range ch {
			if ev.Done {
				final = ev
			}
		}
		if final.Err != nil {
			return modelCheckResultMsg{model: name, err: final.Err}
		}
		return modelCheckResultMsg{model: name, reply: final.Message.Content}
	}
}

func (m Model) handleModelCheckResult(msg modelCheckResultMsg) (tea.Model, tea.Cmd) {
	m.state = stateInput
	if msg.err != nil {
		m.appendLine(errorStyle.Render(fmt.Sprintf("✗ %s: %s", msg.model, msg.err.Error())))
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
	return m, compact(m.client, m.messages)
}

func (m Model) handleCompactResult(msg compactResultMsg) (tea.Model, tea.Cmd) {
	m.state = stateInput
	m.tokensIn += msg.usage.InputTokens
	m.tokensOut += msg.usage.OutputTokens

	if msg.err != nil {
		m.appendLine(errorStyle.Render("compact failed: " + msg.err.Error()))
		return m, nil
	}

	m.messages = compactedHistory(msg.summary)
	m.lines = nil
	m.viewport.SetContent("")
	m.appendLine(dimStyle.Render("── conversation compacted ──"))
	m.appendLine(assistantStyle.Render("chisel  ") + renderAssistantText(msg.summary, m.showThinking))

	return m, saveSession(m.workDir, m.messages)
}
