package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// entry is one line of the transcript, stored so it can be re-rendered —
// re-wrapped on resize, re-collapsed/expanded on /think — rather than
// only ever rendered once at append time and then frozen as a plain
// string, which is what made both of those impossible before.
type entry struct {
	// styled is the ready-to-display line for everything except live
	// assistant text — already includes any prefix and its styling.
	// Wrapping to the current terminal width happens fresh every time the
	// transcript is rendered (see Model.transcriptContent), not baked in
	// here, so a resize doesn't need anything recomputed on this field.
	styled string

	// isAssistant marks an entry whose content is instead derived via
	// renderAssistantText(raw, showThinking) each time render is called —
	// raw may contain <think>...</think>, and whether that's shown
	// collapsed or expanded isn't decided until render time, so toggling
	// /think re-renders every past assistant message, not just new ones.
	isAssistant bool
	raw         string
}

// render returns e's current display form, given the current /think
// setting. Not yet wrapped to any particular width — see wrapToWidth.
func (e entry) render(showThinking bool) string {
	if e.isAssistant {
		return assistantStyle.Render("chisel  ") + renderAssistantText(e.raw, showThinking)
	}
	return e.styled
}

// wrapToWidth word-wraps s to width columns, preserving any ANSI styling
// already embedded in it (lipgloss computes wrap points from the visible
// text, not raw bytes, so escape sequences survive intact). width <= 0
// means "not known yet" (before the first WindowSizeMsg arrives) — in
// that case s is returned unwrapped rather than collapsed to nothing.
func wrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// transcriptContent renders every entry to its current display form and
// wraps each to m.width, joining them into what the viewport shows.
// Called whenever entries change, the terminal resizes, or /think
// toggles — any of which can change what the exact same entries produce.
func (m Model) transcriptContent() string {
	lines := make([]string, len(m.entries))
	for i, e := range m.entries {
		lines[i] = wrapToWidth(e.render(m.showThinking), m.width)
	}
	return strings.Join(lines, "\n")
}

// renderedLines returns each entry's current display form, unwrapped —
// for callers (mainly tests) that want to assert on line content without
// reasoning about word-wrap boundaries.
func (m Model) renderedLines() []string {
	lines := make([]string, len(m.entries))
	for i, e := range m.entries {
		lines[i] = e.render(m.showThinking)
	}
	return lines
}

// refreshViewport rebuilds the viewport's content from the current
// entries. Call after appending, after /think toggles, or after a resize
// changes m.width.
func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.transcriptContent())
}
