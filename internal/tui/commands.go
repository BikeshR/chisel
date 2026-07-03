package tui

import (
	"strings"

	"github.com/BikeshR/chisel/internal/agent"
)

// handleCommand processes a "/"-prefixed line from the input box instead of
// sending it to the model. It always returns to stateInput — commands are
// synchronous and never touch the network.
func (m Model) handleCommand(text string) Model {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/model":
		return m.handleModelCommand(fields[1:])
	case "/think":
		return m.handleThinkCommand()
	default:
		m.appendLine(errorStyle.Render("unknown command: " + fields[0]))
		return m
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

func (m Model) handleModelCommand(args []string) Model {
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
		m.appendLine(dimStyle.Render("  usage: /model <name>"))
		return m
	}

	name := args[0]
	m.client = agent.New(name)
	m.appendLine(dimStyle.Render("switched to " + name))
	return m
}
