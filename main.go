// Command chisel is a terminal coding agent: a Bubbletea TUI wrapped around
// an OpenCode Go model with a fixed tool set (bash, file editing, glob,
// grep).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/tui"
)

// defaultModel is used when CHISEL_MODEL isn't set. Confirmed working via
// direct testing — see docs/design.md.
const defaultModel = "minimax-m3"

func main() {
	loadDotEnv()

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: resolve working directory:", err)
		os.Exit(1)
	}

	model := defaultModel
	if m := os.Getenv("CHISEL_MODEL"); m != "" {
		model = m
	}

	client := agent.New(model)
	tuiModel := tui.New(client, workDir)

	if _, err := tea.NewProgram(tuiModel, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chisel:", err)
		os.Exit(1)
	}
}

// loadDotEnv sets environment variables from ~/.chisel.env if the file
// exists — a simple KEY=value list, "#" comments allowed. It lives outside
// the repo (never committed, never at risk of being pushed) and lets
// chisel's model/base-URL/key be swapped per-machine without editing shell
// profiles or code. Values already set in the real environment take
// precedence over the file. Missing file, or any read error, is silently
// ignored — this config is entirely optional.
func loadDotEnv() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	data, err := os.ReadFile(filepath.Join(home, ".chisel.env"))
	if err != nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, set := os.LookupEnv(key); set {
			continue // real environment wins
		}
		_ = os.Setenv(key, strings.TrimSpace(value)) // key came from our own trusted config file
	}
}
