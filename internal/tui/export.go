package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BikeshR/chisel/internal/agent"
)

// handleExportCommand implements /export [path]: writes the current
// conversation to a markdown file — opencode's /export and Aider's
// /copy-context both cover this same "get the whole session out of the
// terminal" need; chisel already has mouse-selection + OSC-52 copy for
// a snippet, but nothing for the whole thing at once. Renders from
// m.messages (the clean semantic history) rather than m.entries (which
// holds ANSI-styled strings meant for the terminal, not a file).
func (m Model) handleExportCommand(args []string) Model {
	if len(m.messages) == 0 {
		m.appendLine(dimStyle.Render("nothing to export yet"))
		return m
	}
	path := defaultExportPath()
	if len(args) > 0 {
		path = args[0]
	}

	if err := os.WriteFile(path, []byte(renderTranscriptMarkdown(m.messages)), 0o644); err != nil {
		m.appendLine(errorStyle.Render("export: " + err.Error()))
		return m
	}
	m.appendLine(dimStyle.Render("exported conversation to " + path))
	return m
}

// defaultExportPath names the file /export writes when no path is
// given — in the current directory (not .chisel/, which is chisel's
// own config dir), findable with a plain ls since an export is
// something the user asked for and presumably wants to actually see.
func defaultExportPath() string {
	return "chisel-export-" + time.Now().Format("20060102-150405") + ".md"
}

// renderTranscriptMarkdown turns messages into a readable markdown
// document — a tool call gets agent.Summarize's own one-line
// description (the same friendly text the permission prompt shows)
// plus its full arguments as a JSON block, so the export is scannable
// without losing detail. m.messages only ever holds user/assistant/tool
// roles — the system prompt is built fresh per-request in
// agent.Client.SendStreaming, never stored in the conversation history
// itself, so there's no system-role case to render here.
func renderTranscriptMarkdown(messages []agent.Message) string {
	var b strings.Builder
	b.WriteString("# chisel conversation export\n\n")
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			b.WriteString("## You\n\n")
			b.WriteString(msg.Content)
			b.WriteString("\n\n")
		case "assistant":
			b.WriteString("## Assistant\n\n")
			if msg.Content != "" {
				b.WriteString(msg.Content)
				b.WriteString("\n\n")
			}
			for _, call := range msg.ToolCalls {
				fmt.Fprintf(&b, "**Tool call:** %s\n\n```json\n%s\n```\n\n", agent.Summarize(call), call.Function.Arguments)
			}
		case "tool":
			b.WriteString("**Tool result:**\n\n```\n")
			b.WriteString(msg.Content)
			b.WriteString("\n```\n\n")
		}
	}
	return b.String()
}
