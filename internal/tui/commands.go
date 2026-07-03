package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
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
