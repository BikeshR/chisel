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
	context := formatTokenCount(m.lastContextTokens) + " tok"
	if m.lastContextTokens >= contextWarnThreshold {
		context = errorStyle.Render(context + " — large, consider /compact")
	}

	plan := ""
	if m.client.PlanMode() {
		plan = planModeStyle.Render("PLAN MODE") + " · "
	}

	return fmt.Sprintf(" %s%s · context %s · spent %s in / %s out · ctrl+c to quit",
		plan, m.client.ModelName(), context, formatTokenCount(m.tokensIn), formatTokenCount(m.tokensOut))
}
