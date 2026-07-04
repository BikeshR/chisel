package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

// renderHistory reconstructs transcript lines from a resumed conversation,
// following the same per-role rendering conventions the live session uses
// (see update.go) — but without replaying the permission-prompt step,
// since every one of these tool calls was already resolved in a past run.
func renderHistory(messages []agent.Message, showThinking bool) []string {
	var lines []string
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			lines = append(lines, userStyle.Render("you  ")+msg.Content)

		case "assistant":
			if msg.Content != "" {
				lines = append(lines, assistantStyle.Render("chisel  ")+renderAssistantText(msg.Content, showThinking))
			}
			for _, call := range msg.ToolCalls {
				lines = append(lines, toolStyle.Render("  "+summarizeCall(call)))
			}

		case "tool":
			if rest, isErr := strings.CutPrefix(msg.Content, agent.ErrorContentPrefix); isErr {
				lines = append(lines, errorStyle.Render("  ✗ "+firstLine(rest)))
			} else {
				lines = append(lines, dimStyle.Render("  ✓ "+firstLine(msg.Content)))
			}
		}
	}
	return lines
}

// resumeBanner is the informational line shown above a reconstructed
// history, so it's clear at a glance the transcript wasn't typed just now.
func resumeBanner(messageCount int, savedAt time.Time) string {
	return dimStyle.Render(fmt.Sprintf("── resumed session from %s (%d messages) ──",
		humanizeSince(savedAt), messageCount))
}

func humanizeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
