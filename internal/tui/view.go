package tui

import (
	"fmt"
	"strings"

	"github.com/BikeshR/chisel/internal/mcp"
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
		b.WriteString(m.textArea.View())
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

	mcpWarning := ""
	if broken := brokenMCPCount(m.mcp.Statuses()); broken > 0 {
		mcpWarning = errorStyle.Render(fmt.Sprintf("%d mcp broken", broken)) + " · "
	}

	return fmt.Sprintf(" %s%s%s · context %s · spent %s in / %s out · ctrl+c to quit",
		plan, mcpWarning, m.client.ModelName(), context, formatTokenCount(m.tokensIn), formatTokenCount(m.tokensOut))
}

// brokenMCPCount counts how many of statuses are currently broken —
// shown in the status bar so a dead MCP server (previously only ever
// visible via /status, checked on demand) is visible at a glance.
func brokenMCPCount(statuses []mcp.ServerStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Broken {
			n++
		}
	}
	return n
}
