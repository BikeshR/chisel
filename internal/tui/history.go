package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

// renderHistory reconstructs transcript entries from a resumed
// conversation, following the same per-role rendering conventions the
// live session uses (see update.go) — but without replaying the
// permission-prompt step, since every one of these tool calls was
// already resolved in a past run. Assistant messages become isAssistant
// entries (raw, uncollapsed) rather than pre-rendered text, same as a
// live turn — so /think toggling affects resumed history exactly the
// same way it affects anything said in the current session.
func renderHistory(messages []agent.Message) []entry {
	var entries []entry
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			entries = append(entries, entry{styled: userStyle.Render("you  ") + msg.Content})

		case "assistant":
			if msg.Content != "" {
				entries = append(entries, entry{isAssistant: true, raw: msg.Content})
			}
			for _, call := range msg.ToolCalls {
				entries = append(entries, entry{styled: toolStyle.Render("  " + summarizeCall(call))})
			}

		case "tool":
			if rest, isErr := strings.CutPrefix(msg.Content, agent.ErrorContentPrefix); isErr {
				entries = append(entries, entry{styled: errorStyle.Render("  ✗ " + firstLine(rest))})
			} else {
				entries = append(entries, entry{styled: dimStyle.Render("  ✓ " + firstLine(msg.Content))})
			}
		}
	}
	return entries
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
