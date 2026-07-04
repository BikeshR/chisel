package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// assistantPrefixWidth is the visible width of "chisel  ", the prefix
// every assistant line is rendered with — subtracted from the terminal
// width before handing a wrap width to glamour, so its own word-wrap
// (and the line-padding it bakes into its output) leaves room for that
// prefix on the first line instead of running past the terminal edge.
const assistantPrefixWidth = len("chisel  ")

// minMarkdownWrapWidth is a floor under assistantPrefixWidth for a very
// narrow terminal — glamour given a width of zero or less falls back to
// an internal default rather than actually being narrow, which would
// look wrong next to everything else genuinely wrapped to m.width.
const minMarkdownWrapWidth = 20

// newMarkdownRenderer builds a glamour renderer sized for the current
// terminal width, or nil if width isn't known yet (before the first
// WindowSizeMsg — see wrapToWidth's own width<=0 handling for the same
// reasoning) or construction fails for any reason.
//
// Deliberately reuses lipgloss's own color-profile/dark-background
// detection (lipgloss.ColorProfile/HasDarkBackground — the same calls
// every lipgloss.AdaptiveColor in styles.go resolves against) rather
// than glamour's own WithAutoStyle, which runs an independent termenv
// check and disagreed with lipgloss's in a non-tty test environment —
// under go test, lipgloss still reported a full color profile while
// glamour's own detection degraded to a plain, unstyled "notty" style.
// Since chisel only ever runs as a real interactive terminal program,
// that mismatch shouldn't matter in practice, but there's no reason for
// markdown output to risk disagreeing with the rest of chisel's palette
// on light-vs-dark when one detection call already drives everything else.
func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width <= 0 {
		return nil, errWidthUnknown
	}
	wrapWidth := width - assistantPrefixWidth
	if wrapWidth < minMarkdownWrapWidth {
		wrapWidth = minMarkdownWrapWidth
	}
	style := "dark"
	if !lipgloss.HasDarkBackground() {
		style = "light"
	}
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithColorProfile(lipgloss.ColorProfile()),
		glamour.WithWordWrap(wrapWidth),
	)
}

var errWidthUnknown = errors.New("terminal width not yet known")

// renderMarkdownEntry renders a completed (non-streaming) assistant
// entry's content as markdown, returning ok=false whenever that isn't
// safe or possible: no renderer available, or raw contains a <think>
// tag — renderAssistantText's collapse/expand logic produces its own
// dim-styled placeholder text mixed with the model's plain-text content,
// and running that mixture through a markdown parser would treat the
// placeholder's ANSI escapes as literal document text rather than
// leaving them alone. On any success, the result already includes the
// "chisel  " prefix and is fully wrapped (and padded) by glamour itself
// at a width already accounting for that prefix — callers must not wrap
// it again, the same reasoning entry.noRewrap exists for the bordered
// permission box.
func renderMarkdownEntry(e entry, r *glamour.TermRenderer) (string, bool) {
	if r == nil || strings.Contains(e.raw, thinkOpenTag) {
		return "", false
	}
	out, err := r.Render(e.raw)
	if err != nil {
		return "", false
	}
	return assistantStyle.Render("chisel  ") + strings.Trim(out, "\n"), true
}
