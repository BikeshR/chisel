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
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/memory"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/tui"
)

// defaultModel is used when CHISEL_MODEL isn't set. Confirmed working via
// direct testing — see docs/design.md.
const defaultModel = "minimax-m3"

func main() {
	loadDotEnv()

	// Checked here, not left to surface as the first request's raw 401 —
	// that failure mode gives no indication of what's actually wrong,
	// especially once it's buried behind a spinner and a stream error.
	if os.Getenv("CHISEL_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "chisel: CHISEL_API_KEY is not set — set it in your environment or in ~/.chisel.env")
		os.Exit(1)
	}

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
	bash := agent.NewBashSession(workDir)
	defer bash.Close()

	mcpRegistry, mcpErrs := mcp.LoadAndStartAll()
	defer mcpRegistry.Close()
	for _, e := range mcpErrs {
		fmt.Fprintln(os.Stderr, "chisel: mcp:", e)
	}
	client.AddTools(agentToolsFromMCP(mcpRegistry.Tools()))

	hooksCfg, _, err := hooks.LoadConfig(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: hooks:", err)
	}

	memContent, memUser, memProject := memory.Load(workDir)
	if memContent != "" {
		client.SetMemory(memContent)
	}

	resumed, savedAt, _ := session.Load(workDir)
	tuiModel := tui.New(client, workDir, bash, mcpRegistry, hooksCfg, memUser, memProject, resumed, savedAt)

	if _, err := tea.NewProgram(tuiModel, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chisel:", err)
		os.Exit(1)
	}
}

// agentToolsFromMCP converts MCP's own tool shape to agent.Tool — kept
// here rather than in internal/mcp so that package stays a standalone
// protocol client with no dependency on chisel's own wire-format types.
func agentToolsFromMCP(tools []mcp.Tool) []agent.Tool {
	out := make([]agent.Tool, len(tools))
	for i, t := range tools {
		out[i] = agent.Tool{
			Type: "function",
			Function: agent.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return out
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
		_ = os.Setenv(key, unquote(strings.TrimSpace(value))) // key came from our own trusted config file
	}
}

// unquote strips one layer of matching surrounding quotes (single or
// double) from s, if present — CHISEL_API_KEY="sk-..." in ~/.chisel.env
// is a natural thing to write (shell-style, and what most .env-adjacent
// tooling accepts), but without this the literal quote characters would
// end up as part of the value and every request would fail
// authentication with no clue why.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
