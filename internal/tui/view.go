package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/BikeshR/chisel/internal/mcp"
)

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")

	if todos := wrapToWidth(renderTodos(m.todos), m.width); todos != "" {
		b.WriteString(todos)
		b.WriteString("\n")
	}

	switch m.state {
	case stateAwaitingPermission:
		// The prompt itself was already appended to the transcript above;
		// nothing extra goes on the input line while we wait for a decision.
		b.WriteString(dimStyle.Render(m.permissionHint))
	case stateWaitingModel, stateExecutingTool:
		// The textarea stays visible (at a reduced height, so the total
		// footprint still matches recomputeViewportHeight's fixed
		// reservation) rather than being replaced outright — otherwise
		// anything typed here (to be queued once chisel is free again) was
		// completely invisible while composing it.
		b.WriteString(m.busyLine())
		b.WriteString("\n")
		ta := m.textArea
		ta.SetHeight(inputHeight - 1)
		b.WriteString(ta.View())
	default:
		b.WriteString(m.textArea.View())
	}
	b.WriteString("\n")

	b.WriteString(statusBarStyle.Width(m.width).Render(m.statusLine(m.width)))
	return b.String()
}

// statusLine renders the status bar text, dropping optional segments —
// least important first — until it fits width. Without this,
// statusBarStyle.Width's default wrap-when-too-long behavior would break
// the fixed 3-line input/status layout on a narrow terminal (see
// recomputeViewportHeight, which never accounts for a wrapped status bar).
// busyLine renders the spinner line shown during stateWaitingModel/
// stateExecutingTool: what's running (the tool name during a tool call,
// nothing more specific during a model request), how long it's been
// running, and the esc-to-interrupt hint — previously a static "thinking…"
// /"running…" with none of that.
func (m Model) busyLine() string {
	label := "thinking"
	if m.state == stateExecutingTool && len(m.pendingUses) > 0 {
		label = summarizeCall(m.pendingUses[0])
	}
	elapsed := time.Since(m.turnStartedAt).Round(time.Second)
	return fmt.Sprintf("%s %s… (%s · esc to interrupt)", m.spinner.View(), label, elapsed)
}

func (m Model) statusLine(width int) string {
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

	queued := ""
	if n := len(m.queuedMessages); n > 0 {
		queued = dimStyle.Render(fmt.Sprintf("%d queued", n)) + " · "
	}

	background := ""
	if n := runningBackgroundCount(m.backgroundTasks); n > 0 {
		background = dimStyle.Render(fmt.Sprintf("%d bg running", n)) + " · "
	}

	tail := fmt.Sprintf("%s · context %s · spent %s in / %s out · ctrl+c to quit",
		m.client.ModelName(), context, formatTokenCount(m.tokensIn), formatTokenCount(m.tokensOut))

	// Drop segments least important to see at a glance first: background,
	// then queued, then the mcp warning (still visible via /status even
	// when dropped here). Plan mode and the core stats are never dropped.
	optional := []string{background, queued, mcpWarning}
	for drop := 0; drop <= len(optional); drop++ {
		line := " " + plan
		for i, seg := range optional {
			if i >= drop {
				line += seg
			}
		}
		line += tail
		if lipgloss.Width(line) <= width || drop == len(optional) {
			if lipgloss.Width(line) > width {
				return lipgloss.NewStyle().MaxWidth(width).Render(line)
			}
			return line
		}
	}
	return tail
}

// runningBackgroundCount counts how many background tasks are still
// running — shown in the status bar as a live-at-a-glance indicator,
// the same reasoning as brokenMCPCount below.
func runningBackgroundCount(tasks map[string]*backgroundTask) int {
	n := 0
	for _, t := range tasks {
		if t.running {
			n++
		}
	}
	return n
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
