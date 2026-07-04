package tui

import "github.com/charmbracelet/lipgloss"

var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "25", Dark: "75"}).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "236", Dark: "252"})
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "100", Dark: "143"})
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "241"})

	permissionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "223"}).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "136", Dark: "223"}).
			Padding(0, 1)

	// planModeStyle is for the inline status-bar tag — same color as
	// permissionStyle (both mean "chisel is holding back from acting
	// without you"), but no border: a bordered box would break the status
	// bar's single-line layout.
	planModeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "223"}).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "241"}).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "237"})
)
