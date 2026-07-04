// Command chisel is a terminal coding agent: a Bubbletea TUI wrapped around
// an OpenCode Go model with a fixed tool set (bash, file editing, glob,
// grep).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/checkpoint"
	"github.com/BikeshR/chisel/internal/customcmd"
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

	prompt := flag.String("p", "", "run non-interactively: send this prompt, print the final answer, and exit — read-only tools only (no bash, no edits, no MCP), since there's no terminal here to show a permission prompt to")
	flag.Parse()

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

	if *prompt != "" {
		runHeadless(workDir, model, *prompt)
		return
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

	hooksCfg, hooksFound, err := hooks.LoadConfig(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: hooks:", err)
	}
	if hooksFound && hooksCfg.HasAny() {
		if !confirmHooksTrust(workDir) {
			fmt.Fprintln(os.Stderr, "chisel: hooks not trusted — running this session without them")
			hooksCfg = hooks.Config{}
		}
	}

	memContent, memUser, memProject := memory.Load(workDir)
	if memContent != "" {
		client.SetMemory(memContent)
	}

	resumed, savedAt, _ := session.Load(workDir)
	customCommands := customcmd.Load(workDir)
	checkpointStore, err := checkpoint.Open(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: checkpoints unavailable:", err)
	}
	tuiModel := tui.New(client, workDir, bash, mcpRegistry, hooksCfg, memUser, memProject, customCommands, checkpointStore, resumed, savedAt)

	if _, err := tea.NewProgram(tuiModel, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chisel:", err)
		os.Exit(1)
	}
}

// maxHeadlessTurns bounds chisel -p's own tool-calling loop — the same
// safety net a subagent's loop has (see agent.RunLoop), since headless
// mode reuses that exact loop and the same read-only tool set.
const maxHeadlessTurns = 15

// runHeadless is chisel -p: a non-interactive invocation for scripts
// and CI. Prints the model's final answer to stdout and exits;
// non-zero on any failure. Just a thin process-exiting wrapper around
// runHeadlessCore, so a test can call that directly instead.
func runHeadless(workDir, model, prompt string) {
	answer, err := runHeadlessCore(workDir, model, prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel:", err)
		os.Exit(1)
	}
	fmt.Println(answer)
}

// runHeadlessCore does the actual work for chisel -p. Restricted to
// agent.ReadOnlyTools (glob, grep, view) — no bash, no edits, no MCP
// tools (which always need permission) — since there's no terminal
// here to show a permission prompt to, so nothing offered can need one
// in the first place; the same guarantee a subagent's tool set gives,
// reused via the same agent.RunLoop. Hooks aren't loaded either — the
// interactive trust prompt they'd otherwise need doesn't make sense in
// a non-interactive invocation.
func runHeadlessCore(workDir, model, prompt string) (string, error) {
	client := agent.New(model)
	client.SetTools(agent.ReadOnlyTools())

	if memContent, _, _ := memory.Load(workDir); memContent != "" {
		client.SetMemory(memContent)
	}

	ctx := context.Background()
	history := []agent.Message{{Role: "user", Content: prompt}}
	answer, _, err := agent.RunLoop(ctx, client, history, maxHeadlessTurns, func(call agent.ToolCall) agent.ToolResult {
		return agent.Execute(ctx, workDir, model, call, nil)
	})
	return answer, err
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

// confirmHooksTrust asks the user, once per exact hooks.json content,
// whether chisel may run a project's configured hooks — arbitrary shell
// commands that would otherwise run automatically on tool calls,
// including auto-allowed ones like glob/grep that need no confirmation
// at all. Cloning a hostile repo and asking chisel something as
// innocuous as "what does this project do?" would otherwise be enough
// to execute whatever .chisel/hooks.json says. A read or trust-store
// error is treated as "not trusted" — hooks are a convenience, not
// something worth risking a broken prompt over.
func confirmHooksTrust(workDir string) bool {
	return confirmHooksTrustFrom(workDir, os.Stdin)
}

// confirmHooksTrustFrom is confirmHooksTrust with its input source
// injectable, so a test can supply a fake answer instead of reading a
// real terminal's stdin.
func confirmHooksTrustFrom(workDir string, in io.Reader) bool {
	path := hooks.ConfigPath(workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	hash := hooks.ContentHash(data)

	trusted, err := hooks.IsTrusted(hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: checking hooks trust:", err)
		return false
	}
	if trusted {
		return true
	}

	fmt.Printf("chisel: %s configures hooks — shell commands that run automatically on tool calls, some of which (glob, grep) normally need no confirmation at all.\n", path)
	fmt.Print("Trust and run them for this project? [y/N] ")

	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return false
	}

	if err := hooks.Trust(hash); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: saving hooks trust decision:", err)
	}
	return true
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
