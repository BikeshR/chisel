package tui

import (
	"fmt"
	"strings"
)

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	switch m.state {
	case stateAwaitingPermission:
		// The prompt itself was already appended to the transcript above;
		// nothing extra goes on the input line while we wait for y/n.
		b.WriteString(dimStyle.Render("(y/n)"))
	case stateWaitingModel:
		b.WriteString(m.spinner.View() + " thinking…")
	case stateExecutingTool:
		b.WriteString(m.spinner.View() + " running…")
	default:
		b.WriteString(m.textInput.View())
	}
	b.WriteString("\n")

	b.WriteString(statusBarStyle.Width(m.width).Render(m.statusLine()))
	return b.String()
}

func (m Model) statusLine() string {
	return fmt.Sprintf(" %s · in %d / out %d tokens · ctrl+c to quit",
		m.client.ModelName(), m.tokensIn, m.tokensOut)
}
