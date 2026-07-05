package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/mcp"
)

// handleMCPPromptsCommand implements /mcp-prompts: lists every MCP
// prompt template available across running servers — most servers only
// offer tools, so an empty list here is the common case, not an error.
func (m Model) handleMCPPromptsCommand() Model {
	prompts := m.mcp.Prompts()
	if len(prompts) == 0 {
		m.appendLine(dimStyle.Render("no MCP prompts available"))
		return m
	}
	var b strings.Builder
	b.WriteString("MCP prompts:\n")
	for _, p := range prompts {
		fmt.Fprintf(&b, "  %s:%s — %s\n", p.Server, p.Name, p.Description)
	}
	b.WriteString("type /mcp-prompt <server> <name> [key=value ...] to use one")
	m.appendLine(dimStyle.Render(b.String()))
	return m
}

// handleMCPResourcesCommand is /mcp-prompts' exact counterpart for
// resources/list.
func (m Model) handleMCPResourcesCommand() Model {
	resources := m.mcp.Resources()
	if len(resources) == 0 {
		m.appendLine(dimStyle.Render("no MCP resources available"))
		return m
	}
	var b strings.Builder
	b.WriteString("MCP resources:\n")
	for _, r := range resources {
		fmt.Fprintf(&b, "  %s:%s — %s\n", r.Server, r.URI, r.Name)
	}
	b.WriteString("type /mcp-resource <server> <uri> to load one into the conversation")
	m.appendLine(dimStyle.Render(b.String()))
	return m
}

// mcpPromptResultMsg carries the outcome of fetching and expanding an
// MCP prompt template — see fetchMCPPrompt. Async because prompts/get
// is a real subprocess round-trip through the server, unlike a local
// custom command's plain string substitution (customcmd.Expand).
type mcpPromptResultMsg struct {
	text string
	err  error
}

func fetchMCPPrompt(ctx context.Context, registry *mcp.Registry, server, name string, arguments map[string]string) tea.Cmd {
	return func() tea.Msg {
		text, err := registry.GetPrompt(ctx, server, name, arguments)
		return mcpPromptResultMsg{text: text, err: err}
	}
}

// handleMCPPromptCommand implements /mcp-prompt <server> <name>
// [key=value ...] — fetches the expanded template from the server and
// submits it exactly like a typed message (through
// submitTextWithHookCheck, so a configured UserPromptSubmit hook still
// applies to server-supplied text the same way it does to anything
// else reaching the model).
func (m Model) handleMCPPromptCommand(args []string) (Model, tea.Cmd) {
	if len(args) < 2 {
		m.appendLine(errorStyle.Render("usage: /mcp-prompt <server> <name> [key=value ...] — /mcp-prompts lists what's available"))
		return m, nil
	}
	server, name := args[0], args[1]
	arguments := map[string]string{}
	for _, kv := range args[2:] {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			m.appendLine(errorStyle.Render("arguments must be key=value: " + kv))
			return m, nil
		}
		arguments[key] = value
	}

	m.appendLine(dimStyle.Render(fmt.Sprintf("fetching prompt %s:%s…", server, name)))
	m.startBusy(stateWaitingModel)
	return m, fetchMCPPrompt(m.newTurnContext(), m.mcp, server, name, arguments)
}

// handleMCPPromptResult either submits the fetched prompt text as a new
// turn or reports why fetching it failed, returning to stateInput either way.
func (m Model) handleMCPPromptResult(msg mcpPromptResultMsg) (Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	if msg.err != nil {
		m.appendLine(errorStyle.Render("mcp prompt: " + msg.err.Error()))
		return m, m.turnSettledCmd()
	}
	return m.submitTextWithHookCheck(msg.text)
}

// mcpResourceResultMsg carries the outcome of fetching an MCP resource
// — see fetchMCPResource.
type mcpResourceResultMsg struct {
	server, uri, content string
	err                  error
}

func fetchMCPResource(ctx context.Context, registry *mcp.Registry, server, uri string) tea.Cmd {
	return func() tea.Msg {
		content, err := registry.ReadResource(ctx, server, uri)
		return mcpResourceResultMsg{server: server, uri: uri, content: content, err: err}
	}
}

// handleMCPResourceCommand implements /mcp-resource <server> <uri> —
// fetches the resource's content and submits it framed the same way
// piped stdin is in headless mode, so the model can tell where the
// injected content starts and ends.
func (m Model) handleMCPResourceCommand(args []string) (Model, tea.Cmd) {
	if len(args) < 2 {
		m.appendLine(errorStyle.Render("usage: /mcp-resource <server> <uri> — /mcp-resources lists what's available"))
		return m, nil
	}
	server, uri := args[0], args[1]

	m.appendLine(dimStyle.Render(fmt.Sprintf("fetching resource %s:%s…", server, uri)))
	m.startBusy(stateWaitingModel)
	return m, fetchMCPResource(m.newTurnContext(), m.mcp, server, uri)
}

// handleMCPResourceResult either submits the fetched resource content
// (framed with its origin) as a new turn or reports why fetching it
// failed.
func (m Model) handleMCPResourceResult(msg mcpResourceResultMsg) (Model, tea.Cmd) {
	m.endTurn()
	m.state = stateInput
	if msg.err != nil {
		m.appendLine(errorStyle.Render("mcp resource: " + msg.err.Error()))
		return m, m.turnSettledCmd()
	}
	framed := fmt.Sprintf("--- MCP resource %s:%s ---\n%s\n--- end resource ---", msg.server, msg.uri, msg.content)
	return m.submitTextWithHookCheck(framed)
}
