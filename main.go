// Command chisel is a terminal coding agent: a Bubbletea TUI wrapped around
// an OpenCode Go model with a fixed tool set (bash, file editing, glob,
// grep).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/checkpoint"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/memory"
	"github.com/BikeshR/chisel/internal/permrules"
	"github.com/BikeshR/chisel/internal/session"
	"github.com/BikeshR/chisel/internal/skill"
	"github.com/BikeshR/chisel/internal/tui"
)

// defaultModel is used when CHISEL_MODEL isn't set. Confirmed working via
// direct testing — see docs/design.md.
const defaultModel = "minimax-m3"

// version identifies this build — override at release-build time via
// -ldflags "-X main.version=v1.2.3"; a plain `go build`/`go install` (the
// common case for this personal tool, with no release pipeline) leaves
// it at "dev", which still beats having no way at all to tell which
// build is actually running.
var version = "dev"

func main() {
	loadDotEnv()

	prompt := flag.String("p", "", "run non-interactively: send this prompt, print the final answer, and exit — read-only tools only (no bash, no edits, no MCP), since there's no terminal here to show a permission prompt to. Piped stdin (e.g. 'git diff | chisel -p \"review this\"') is appended to the prompt. Put -p last (or use -p=\"...\"): being a string flag, it otherwise swallows the next flag as its own value")
	jsonOutput := flag.Bool("json", false, "with -p, emit a single JSON object (answer, usage, error) to stdout instead of plain text — for scripts and CI")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("chisel", version)
		return
	}

	// -json only changes headless (-p) output — silently doing nothing
	// with it otherwise (the previous behavior) left no indication the
	// flag was even noticed, let alone why it had no effect.
	if *jsonOutput && *prompt == "" {
		fmt.Fprintln(os.Stderr, "chisel: -json has no effect without -p — ignoring")
	}

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
		piped, err := readPipedStdin()
		if err != nil {
			fmt.Fprintln(os.Stderr, "chisel: reading piped stdin:", err)
			os.Exit(1)
		}
		runHeadless(workDir, model, *prompt+piped, *jsonOutput)
		return
	}

	client := agent.New(model)
	bash := agent.NewBashSession(workDir)
	defer bash.Close()

	userMCPCfg, _, err := mcp.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: mcp config:", err)
	}
	projectMCPCfg, projectMCPFound, err := mcp.LoadProjectConfig(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: project mcp config:", err)
	}
	if projectMCPFound && len(projectMCPCfg.MCPServers) > 0 {
		if !confirmMCPTrust(workDir, userMCPCfg) {
			fmt.Fprintln(os.Stderr, "chisel: project mcp.json not trusted — running this session without it")
			projectMCPCfg = mcp.Config{}
		}
	}
	mcpRegistry, mcpErrs := mcp.LoadAndStartAll(mcp.Merge(userMCPCfg, projectMCPCfg))
	defer mcpRegistry.Close()
	for _, e := range mcpErrs {
		fmt.Fprintln(os.Stderr, "chisel: mcp:", e)
	}
	maybeAddGopls(mcpRegistry, workDir)
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

	permRules, rulesFound, err := permrules.Load(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: permission rules:", err)
	}
	if rulesFound && permRules.HasAny() {
		if !confirmPermRulesTrust(workDir) {
			fmt.Fprintln(os.Stderr, "chisel: permission rules not trusted — running this session without them")
			permRules = nil
		}
	}

	memContent, memUser, memProject := memory.Load(workDir)
	if memContent != "" {
		client.SetMemory(memContent)
	}

	resumed, savedAt, sessionID, _, sessionLoadFailed := session.LoadLatest(workDir)
	if sessionID == "" {
		sessionID = session.NewID()
	} else if err := session.Save(workDir, sessionID, resumed); err != nil {
		// Re-save immediately, bumping SavedAt out of PruneOld's stale
		// window right below — otherwise resuming a session older than
		// staleSessionAge (returning to a long-dormant project after a
		// while) gets it deleted by that very next call, and quitting
		// before the first turn completes loses the conversation for
		// good even though it was just shown as resumed. The same
		// save-immediately reasoning /new and /resume already apply.
		fmt.Fprintln(os.Stderr, "chisel: saving resumed session:", err)
	}
	// Runs after loading (and re-saving) this directory's own session,
	// not before — so there's no risk of pruning it out from under the
	// very resume that just happened (see PruneOld's own doc comment).
	if _, err := session.PruneOld(); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: pruning old sessions:", err)
	}
	customCommands := customcmd.Load(workDir)
	// Runs before Open, not after — see PruneStaleRepos' own doc comment
	// on why the current project's Store has to be the one doing any
	// necessary recreating, not a prune racing in after it.
	if _, err := checkpoint.PruneStaleRepos(); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: pruning old checkpoint repos:", err)
	}
	checkpointStore, err := checkpoint.Open(workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: checkpoints unavailable:", err)
	}
	skills := skill.Load(workDir)
	client.SetSkills(skills)
	tuiModel := tui.New(client, workDir, bash, mcpRegistry, hooksCfg, memUser, memProject, customCommands, checkpointStore, skills, permRules, resumed, savedAt, sessionLoadFailed, sessionID)

	finalModel, err := tea.NewProgram(tuiModel, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	// A still-running bash_background command has its own context,
	// deliberately independent of any turn's — nothing else stops it
	// when chisel exits, so this is the one place that does.
	if m, ok := finalModel.(tui.Model); ok {
		m.CancelBackgroundTasks()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel:", err)
		os.Exit(1)
	}
}

// maxHeadlessTurns bounds chisel -p's own tool-calling loop — the same
// safety net a subagent's loop has (see agent.RunLoop), since headless
// mode reuses that exact loop and the same read-only tool set.
const maxHeadlessTurns = 15

// headlessResult is chisel -p -json's entire stdout output — one JSON
// object, printed whether the run succeeded or failed, so a script can
// always parse stdout rather than needing to fall back to scraping
// stderr text on failure. The exit code still reflects success/failure
// (0 or 1); Error is just here to make that failure's reason
// machine-readable too, not only human-readable on stderr.
type headlessResult struct {
	Answer string        `json:"answer,omitempty"`
	Usage  headlessUsage `json:"usage"`
	Error  string        `json:"error,omitempty"`
}

type headlessUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// formatHeadlessJSON builds headless mode's JSON stdout line from a
// run's outcome — pulled out of runHeadless as its own pure function
// so a test can check the exact JSON shape without needing to run
// runHeadless as a subprocess just to observe what it prints before
// calling os.Exit.
func formatHeadlessJSON(answer string, usage agent.Usage, runErr error) (string, error) {
	result := headlessResult{
		Answer: answer,
		Usage:  headlessUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens},
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	data, err := json.Marshal(result)
	return string(data), err
}

// runHeadless is chisel -p: a non-interactive invocation for scripts
// and CI. Prints the model's final answer to stdout and exits;
// non-zero on any failure. With jsonOutput, prints one headlessResult
// JSON object instead of plain text — see its own doc comment for why
// that happens on failure too, not just success. Just a thin
// process-exiting wrapper around runHeadlessCore, so a test can call
// that directly instead.
// readPipedStdin returns piped stdin content for headless mode (chisel
// -p), framed so the model can tell where injected content starts and
// ends — the same style @file references use in the interactive TUI
// (see internal/tui/fileref.go's expandFileReferences). Common scripting
// pattern this enables: `git diff | chisel -p "review this"`. Returns ""
// without reading anything if stdin is a real terminal rather than a
// pipe/redirect — checking Stdin.Stat's mode is what avoids blocking
// forever waiting for input that will never come when chisel -p is run
// interactively with nothing piped in.
func readPipedStdin() (string, error) {
	return readPipedInput(os.Stdin)
}

// readPipedInput is readPipedStdin with its source injectable, so a test
// can supply a real os.Pipe() instead of the process's actual stdin.
func readPipedInput(r *os.File) (string, error) {
	info, err := r.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	return fmt.Sprintf("\n\n--- piped stdin ---\n%s\n--- end piped stdin ---\n", data), nil
}

func runHeadless(workDir, model, prompt string, jsonOutput bool) {
	answer, usage, err := runHeadlessCore(workDir, model, prompt)

	if jsonOutput {
		line, marshalErr := formatHeadlessJSON(answer, usage, err)
		if marshalErr != nil {
			// Content is plain strings and int64s — this should never
			// actually fail, but silently printing nothing on stdout
			// would be a worse failure mode than a clear stderr message.
			fmt.Fprintln(os.Stderr, "chisel: marshal headless result:", marshalErr)
			os.Exit(1)
		}
		fmt.Println(line)
		if err != nil {
			os.Exit(1)
		}
		return
	}

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
func runHeadlessCore(workDir, model, prompt string) (string, agent.Usage, error) {
	client := agent.New(model)
	client.SetTools(agent.ReadOnlyTools())

	if memContent, _, _ := memory.Load(workDir); memContent != "" {
		client.SetMemory(memContent)
	}

	ctx := context.Background()
	history := []agent.Message{{Role: "user", Content: prompt}}
	return agent.RunLoop(ctx, client, history, maxHeadlessTurns, func(call agent.ToolCall) agent.ToolResult {
		return agent.Execute(ctx, workDir, model, call, nil, nil)
	})
}

// maybeAddGopls auto-detects a Go project (a go.mod in workDir) with
// gopls installed and, if so, adds it as an MCP server automatically —
// gopls's own "mcp" subcommand (added in golang.org/x/tools/gopls,
// confirmed live: `gopls mcp` speaks real MCP over stdio) exposes real
// Go-aware tools — go_diagnostics (parse/build errors across the
// workspace) and go_symbol_references (find references to a symbol by
// name, not a raw line/column position) chief among them, plus several
// more (go_search, go_package_api, go_rename_symbol, go_vulncheck,
// go_workspace, go_file_context) — with zero LSP-protocol code needed
// in chisel itself: it reuses the exact same MCP client every other
// server already goes through. Deliberately not given any special
// auto-allow treatment the way a hand-written read-only tool would
// get: MCP tools always need permission (see internal/tui/permission.go)
// precisely because chisel can't audit an arbitrary server's tools —
// and go_rename_symbol genuinely does mutate files, so the same rule
// applying uniformly here is correct, not an oversight.
//
// A no-op, not an error, if there's no go.mod, gopls isn't on PATH, or
// the user has already configured a server literally named "gopls"
// themselves — that explicit choice always wins over this automatic one.
func maybeAddGopls(registry *mcp.Registry, workDir string) {
	if _, err := os.Stat(filepath.Join(workDir, "go.mod")); err != nil {
		return
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		return
	}
	if err := registry.AddServer("gopls", mcp.ServerConfig{Command: "gopls", Args: []string{"mcp"}}); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: gopls:", err)
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

// confirmPermRulesTrust is confirmHooksTrust's exact counterpart for
// .chisel/permissions.json — a rule that "allow"s a call can silently
// bypass confirmation the same way a hook can silently execute
// arbitrary code, so loading one needs the same one-time,
// content-hash-keyed approval (internal/permrules, backed by the same
// internal/trust mechanism hooks uses, under its own trust file so
// trusting one never implies trusting the other).
func confirmPermRulesTrust(workDir string) bool {
	return confirmPermRulesTrustFrom(workDir, os.Stdin)
}

func confirmPermRulesTrustFrom(workDir string, in io.Reader) bool {
	path := permrules.ConfigPath(workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	hash := permrules.ContentHash(data)

	trusted, err := permrules.IsTrusted(hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: checking permission rules trust:", err)
		return false
	}
	if trusted {
		return true
	}

	fmt.Printf("chisel: %s configures persistent permission rules — some can silently allow a call (like bash) that would otherwise ask for confirmation every time.\n", path)
	fmt.Print("Trust and apply them for this project? [y/N] ")

	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return false
	}

	if err := permrules.Trust(hash); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: saving permission rules trust decision:", err)
	}
	return true
}

// confirmMCPTrust is confirmHooksTrust's counterpart for a project-scoped
// .chisel/mcp.json — a project-configured MCP server is exactly as
// capable of running arbitrary commands as a hook is (it's launched the
// same way: an arbitrary command plus args), so it needs the same
// one-time, content-hash-keyed approval before chisel starts it. Only
// the project-scoped config is gated — ~/.chisel/mcp.json is the user's
// own, not something a cloned repo could plant.
//
// userCfg (already loaded by the caller) is used only to flag a project
// entry that shadows a user-scoped server of the same name — a cloned
// repo could otherwise plant, say, its own "github" server under a
// name you already trust from your own config, and a generic "this
// configures MCP servers" prompt gave no way to notice the swap.
func confirmMCPTrust(workDir string, userCfg mcp.Config) bool {
	return confirmMCPTrustFrom(workDir, userCfg, os.Stdin)
}

func confirmMCPTrustFrom(workDir string, userCfg mcp.Config, in io.Reader) bool {
	path := mcp.ProjectConfigPath(workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	hash := mcp.ContentHash(data)

	trusted, err := mcp.IsTrusted(hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chisel: checking mcp config trust:", err)
		return false
	}
	if trusted {
		return true
	}

	// Same reasoning chisel's own permission prompt was fixed for: a
	// trust decision with no visibility into what it actually approves
	// isn't an informed one. Best-effort — if the config doesn't even
	// parse, fall back to the generic message; LoadAndStartAll reports
	// the real parse error later regardless.
	var cfg mcp.Config
	if err := json.Unmarshal(data, &cfg); err != nil || len(cfg.MCPServers) == 0 {
		fmt.Printf("chisel: %s configures MCP servers — each one launches an arbitrary command chisel will run and hand tools from to the model.\n", path)
	} else {
		fmt.Printf("chisel: %s configures these MCP servers:\n", path)
		names := make([]string, 0, len(cfg.MCPServers))
		for name := range cfg.MCPServers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sc := cfg.MCPServers[name]
			cmdLine := sc.Command
			if len(sc.Args) > 0 {
				cmdLine += " " + strings.Join(sc.Args, " ")
			}
			marker := ""
			if _, overridden := userCfg.MCPServers[name]; overridden {
				marker = "  (overrides your own user-scoped server of the same name!)"
			}
			fmt.Printf("  %s: %s%s\n", name, cmdLine, marker)
		}
	}
	fmt.Print("Trust and start them for this project? [y/N] ")

	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return false
	}

	if err := mcp.Trust(hash); err != nil {
		fmt.Fprintln(os.Stderr, "chisel: saving mcp config trust decision:", err)
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
