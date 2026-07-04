package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
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

	// noRewrap marks an entry that was already wrapped to the terminal
	// width at construction time and must not be wrapped again here —
	// namely the bordered permission-prompt box (see dispatchNextTool):
	// re-wrapping already-boxed text treats its border-drawing characters
	// as ordinary content and misaligns the right border. A resize while
	// that entry is on screen leaves its box at its original width rather
	// than reflowing, which is an acceptable tradeoff against a corrupted
	// border on every permission prompt.
	noRewrap bool

	// isToolResult marks a tool-call result entry, whose display form
	// toggles between a one-line summary and the full content — see
	// expanded, toggled by ctrl+o (Model.toggleLastToolResult). Previously
	// results only ever showed their first line with no way to see the
	// rest without re-running the call.
	isToolResult bool
	fullContent  string
	resultIsErr  bool
	expanded     bool

	// streaming marks an isAssistant entry still being built from text
	// deltas (see Model.appendStreamText/endStreamLine). Markdown
	// rendering is only attempted once it's false — running a markdown
	// parser against a half-written document every delta would both
	// waste the work (see the cache below) and flicker as formatting
	// guesses change document-to-document.
	streaming bool

	// cacheValid/cachedWidth/cachedShowThinking/cachedRender memoize this
	// entry's fully-wrapped display form — see transcriptContent, which
	// previously re-rendered (and, now, potentially re-ran a markdown
	// parser on) every entry on every call, including ones whose content
	// hasn't changed since the last render. A cache hit requires both the
	// terminal width and /think setting to match what it was computed
	// for; anything that changes an entry's own content (a stream delta,
	// toggling ctrl+o's expanded) must invalidate it directly instead,
	// since neither of those is reflected in the cache key.
	cacheValid         bool
	cachedWidth        int
	cachedShowThinking bool
	cachedRender       string
}

// maxToolResultDisplayChars caps how much of an expanded tool result's
// content is shown at once — independent of agent.maxToolOutputChars,
// which bounds what's sent to the model; this is purely about not
// dumping an unreasonable wall of text into the transcript in one go.
const maxToolResultDisplayChars = 4000

// renderToolResult is entry.render's tool-result case, factored out for
// readability: collapsed shows just the first line (as before); expanded
// shows the full content, capped for display.
func renderToolResult(e entry) string {
	style, marker := dimStyle, "  ✓ "
	if e.resultIsErr {
		style, marker = errorStyle, "  ✗ "
	}
	if !e.expanded {
		return style.Render(marker + firstLine(e.fullContent))
	}
	content := e.fullContent
	if runes := []rune(content); len(runes) > maxToolResultDisplayChars {
		content = string(runes[:maxToolResultDisplayChars]) + fmt.Sprintf("\n… %d more characters (ctrl+o to collapse)", len(runes)-maxToolResultDisplayChars)
	}
	return style.Render(marker) + content
}

// render returns e's current display form, given the current /think
// setting. Not yet wrapped to any particular width — see wrapToWidth.
func (e entry) render(showThinking bool) string {
	if e.isAssistant {
		return assistantStyle.Render("chisel  ") + renderAssistantText(e.raw, showThinking)
	}
	if e.isToolResult {
		return renderToolResult(e)
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
//
// Re-rendering (and, for a completed assistant message, re-parsing as
// markdown — see renderMarkdownEntry) every entry on every call scales
// with the whole transcript's length, not just what changed; that made a
// long session visibly laggy while streaming, since every delta
// re-rendered everything said so far just to add one more line at the
// bottom. The cache on each entry (see its own doc comment) turns this
// back into O(1) amortized per call: only entries whose content or the
// two things this depends on (width, showThinking) actually changed are
// recomputed.
func (m Model) transcriptContent() string {
	var md *glamour.TermRenderer
	mdTried := false
	getMarkdownRenderer := func() *glamour.TermRenderer {
		if !mdTried {
			mdTried = true
			md, _ = newMarkdownRenderer(m.width)
		}
		return md
	}

	lines := make([]string, len(m.entries))
	for i := range m.entries {
		e := &m.entries[i]
		if e.noRewrap {
			lines[i] = e.render(m.showThinking)
			continue
		}
		if e.cacheValid && e.cachedWidth == m.width && e.cachedShowThinking == m.showThinking {
			lines[i] = e.cachedRender
			continue
		}

		rendered, ok := "", false
		if e.isAssistant && !e.streaming {
			rendered, ok = renderMarkdownEntry(*e, getMarkdownRenderer())
		}
		if !ok {
			rendered = wrapToWidth(e.render(m.showThinking), m.width)
		}

		e.cachedRender = rendered
		e.cachedWidth = m.width
		e.cachedShowThinking = m.showThinking
		e.cacheValid = true
		lines[i] = rendered
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
