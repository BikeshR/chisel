package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	diffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "108"})  // muted green
	diffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "174"}) // muted red
)

// maxDiffPreviewLines caps how much of a diff a permission prompt shows
// inline. Even now that the transcript can scroll, a several-hundred-line
// diff dwarfing the actual y/n decision doesn't help anyone read it
// faster — it's meant as a preview to inform the decision, not a full
// review tool.
const maxDiffPreviewLines = 40

// colorizeDiff adds per-line color to a unified diff — content lines
// (not the +++/--- file headers) get a green/red foreground — and caps
// how many lines are shown, appending a count of what was hidden.
func colorizeDiff(diff string) string {
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	shown := lines
	var truncated int
	if len(lines) > maxDiffPreviewLines {
		shown = lines[:maxDiffPreviewLines]
		truncated = len(lines) - maxDiffPreviewLines
	}

	out := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			out = append(out, line)
		case strings.HasPrefix(line, "+"):
			out = append(out, diffAddStyle.Render(line))
		case strings.HasPrefix(line, "-"):
			out = append(out, diffDelStyle.Render(line))
		default:
			out = append(out, line)
		}
	}
	if truncated > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("… %d more lines — approve to apply", truncated)))
	}
	return strings.Join(out, "\n")
}
