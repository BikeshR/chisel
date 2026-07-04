package tui

import "github.com/charmbracelet/lipgloss"

var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("143"))
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	permissionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("223")).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("223")).
			Padding(0, 1)

	// planModeStyle is for the inline status-bar tag — same color as
	// permissionStyle (both mean "chisel is holding back from acting
	// without you"), but no border: a bordered box would break the status
	// bar's single-line layout.
	planModeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("223")).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(lipgloss.Color("237"))
)
