package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/gitutil"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
	"github.com/BikeshR/chisel/internal/session"
)

type state int

const (
	stateInput state = iota
	stateWaitingModel
	stateAwaitingPermission
	stateExecutingTool
)

// Model is chisel's Bubbletea model: it holds the conversation sent to the
// API, the queue of tool calls still to process for the in-flight turn, and
// enough UI state to render the transcript, a spinner, and a permission
// prompt.
type Model struct {
	client  *agent.Client
	workDir string
	bash    *agent.BashSession
	mcp     *mcp.Registry
	hooks   hooks.Config

	messages []agent.Message
	lines    []string // rendered transcript, newest last

	textInput textinput.Model
	viewport  viewport.Model
	spinner   spinner.Model

	state          state
	pendingUses    []agent.ToolCall
	pendingResults []agent.Message // one "tool" role message per completed call

	// streamLineIdx is the index into lines of the assistant line currently
	// being built from streamed text deltas, or -1 if none is in progress.
	streamLineIdx int
	streamText    string
	showThinking  bool // /think toggles this; collapsed by default
	autoCommit    bool // /git auto toggles this; off by default

	tokensIn, tokensOut int64
	// lastContextTokens is the prompt size of the most recent request —
	// unlike tokensIn (a running total across every request this session,
	// for cost tracking), this is "how full is the context window right
	// now", since every request resends the full history.
	lastContextTokens int64
	width, height     int
	err               error
	quitting          bool
}

// New builds the initial Model for a chisel session rooted at workDir.
// bash and mcpRegistry are owned by the caller (main.go), not created
// here, so their lifecycle (closing the shell / MCP server processes on
// exit) doesn't depend on anything inside this package. resumed and
// savedAt come from session.Load — pass a nil/zero pair if there's
// nothing to resume. hooksCfg comes from hooks.LoadConfig — a zero value
// is fine and just means no hooks configured. memUser/memProject report
// which CHISEL.md files memory.Load found, just to show a startup line —
// the content itself was already handed to the client via SetMemory.
func New(client *agent.Client, workDir string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, memUser, memProject bool, resumed []agent.Message, savedAt time.Time) Model {
	ti := textinput.New()
	ti.Placeholder = "ask chisel to do something…"
	ti.Focus()
	ti.CharLimit = 4000

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))

	m := Model{
		client:        client,
		workDir:       workDir,
		bash:          bash,
		mcp:           mcpRegistry,
		hooks:         hooksCfg,
		messages:      resumed,
		textInput:     ti,
		viewport:      viewport.New(80, 20),
		spinner:       sp,
		state:         stateInput,
		streamLineIdx: -1,
	}

	if memUser || memProject {
		m.lines = append(m.lines, dimStyle.Render("loaded "+memoryBannerText(memUser, memProject)))
	}

	if len(resumed) > 0 {
		m.lines = append(m.lines, resumeBanner(len(resumed), savedAt))
		m.lines = append(m.lines, renderHistory(resumed, m.showThinking)...)
	}

	if len(m.lines) > 0 {
		m.viewport.SetContent(joinLines(m.lines))
		m.viewport.GotoBottom()
	}

	return m
}

func memoryBannerText(memUser, memProject bool) string {
	switch {
	case memUser && memProject:
		return "CHISEL.md (user + project)"
	case memProject:
		return "CHISEL.md (project)"
	default:
		return "CHISEL.md (user)"
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m *Model) appendLine(s string) {
	m.lines = append(m.lines, s)
	m.viewport.SetContent(joinLines(m.lines))
	m.viewport.GotoBottom()
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// appendStreamText appends a text delta to the assistant line currently
// being streamed, starting a new line on the first delta of a turn.
func (m *Model) appendStreamText(delta string) {
	if m.streamLineIdx == -1 {
		m.lines = append(m.lines, "")
		m.streamLineIdx = len(m.lines) - 1
		m.streamText = ""
	}
	m.streamText += delta
	m.lines[m.streamLineIdx] = assistantStyle.Render("chisel  ") + renderAssistantText(m.streamText, m.showThinking)
	m.viewport.SetContent(joinLines(m.lines))
	m.viewport.GotoBottom()
}

// endStreamLine closes out the in-progress assistant line so the next text
// block (if any, within the same or a later turn) starts a fresh one.
func (m *Model) endStreamLine() {
	m.streamLineIdx = -1
	m.streamText = ""
}

// executeTool runs a tool call's full lifecycle: preToolUse hooks (which
// can block it outright), the call itself, then postToolUse hooks (whose
// output, if any, is folded into the result so the model sees it). Hooks
// run here rather than as a separate pre-permission-prompt step because
// they're arbitrary shell commands that can take real time (up to
// hooks.hookTimeout) — unlike plan mode's block, which is a plain boolean
// check and cheap enough to do synchronously before the prompt even
// appears, hooks have to go through the same async Cmd as the tool call
// itself. The tradeoff: a hook can still block a call the user already
// approved via the permission prompt, rather than pre-empting the prompt
// entirely — accepted for the simplicity of not needing a second async
// round-trip before every permission decision.
func executeTool(workDir, model string, bash *agent.BashSession, mcpRegistry *mcp.Registry, hooksCfg hooks.Config, call agent.ToolCall) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		path := toolPath(call)

		blocked, reason, err := hooks.RunPreToolUse(ctx, workDir, hooksCfg.Hooks.PreToolUse, call.Function.Name, call.Function.Arguments, path)
		if err != nil {
			return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "pre-tool-use hook: " + err.Error(), IsError: true}}
		}
		if blocked {
			return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: "Blocked by a preToolUse hook: " + reason, IsError: true}}
		}

		var result agent.ToolResult
		if mcp.IsToolName(call.Function.Name) {
			args := json.RawMessage(call.Function.Arguments)
			content, isError, err := mcpRegistry.Call(ctx, call.Function.Name, args)
			if err != nil {
				result = agent.ToolResult{ID: call.ID, Content: err.Error(), IsError: true}
			} else {
				result = agent.ToolResult{ID: call.ID, Content: content, IsError: isError}
			}
		} else {
			result = agent.Execute(ctx, workDir, model, call, bash)
		}

		if !result.IsError {
			if out, err := hooks.RunPostToolUse(ctx, workDir, hooksCfg.Hooks.PostToolUse, call.Function.Name, call.Function.Arguments, path); err != nil {
				result.Content += "\n\n(post-tool-use hook: " + err.Error() + ")"
			} else if out != "" {
				result.Content += "\n\n[hook] " + out
			}
		}

		return toolResultMsg{result: result}
	}
}

// toolPath pulls a "path" argument out of call, if it has one — chisel's
// editor tool always does, and this stays generic (any tool with a "path"
// argument benefits) rather than special-casing by tool name.
func toolPath(call agent.ToolCall) string {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &in)
	return in.Path
}

// summarizeCall renders a permission-prompt-friendly description of call,
// prettifying chisel's mcp__server__tool naming into "server: tool" —
// agent.Summarize itself doesn't know about that convention, by design
// (see internal/mcp's package doc), so this is purely a display-layer
// improvement on top of it.
func summarizeCall(call agent.ToolCall) string {
	if server, tool, ok := mcp.SplitToolName(call.Function.Name); ok {
		return fmt.Sprintf("%s: %s", server, tool)
	}
	return agent.Summarize(call)
}

// needsPermission reports whether call must be confirmed before running.
// Every MCP-sourced tool always needs permission — chisel has no way to
// know what an arbitrary server's tool actually does, so it can't apply
// the same read-only auto-allow heuristic agent.NeedsPermission uses for
// its own fixed tools.
func needsPermission(call agent.ToolCall) bool {
	if mcp.IsToolName(call.Function.Name) {
		return true
	}
	return agent.NeedsPermission(call)
}

// saveSession persists messages as the current session for workDir. A
// failure here isn't fatal to the conversation itself, so it's reported
// as a sessionSaveErrorMsg rather than surfaced through the normal
// error-handling path.
func saveSession(workDir string, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		if err := session.Save(workDir, messages); err != nil {
			return sessionSaveErrorMsg{err: err}
		}
		return nil
	}
}

// autoCommit stages and commits any changes chisel made this turn, if
// /git auto is on. Returning a nil Msg (via the early returns below) is
// deliberate — "nothing to commit" and "not a repo" aren't events worth a
// line in the transcript every single turn.
func autoCommit(workDir string, userText string) tea.Cmd {
	return func() tea.Msg {
		if !gitutil.IsRepo(workDir) {
			return nil
		}
		changed, err := gitutil.HasChanges(workDir)
		if err != nil || !changed {
			return nil
		}
		sha, err := gitutil.CommitAll(workDir, commitMessage(userText))
		return autoCommitResultMsg{sha: sha, err: err}
	}
}

// commitMessage derives a short, git-subject-line-length message from the
// user request that drove this turn's changes.
func commitMessage(userText string) string {
	const maxLen = 72
	subject := firstLine(userText)
	if len(subject) > maxLen {
		subject = subject[:maxLen] + "…"
	}
	return "chisel: " + subject
}

// lastUserText returns the most recent user message in messages, for
// deriving an auto-commit message. Falls back to a generic subject if
// there somehow isn't one (shouldn't happen in practice — every turn
// starts with a user message).
func lastUserText(messages []agent.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return "changes"
}

// compactPrompt asks the model to summarize the conversation so far, for
// /compact. Sent as one more turn through the same client — chisel has no
// server-side compaction to lean on (that's an Anthropic API feature),
// so this is the model doing the summarizing itself.
const compactPrompt = "Summarize this conversation so far in a concise form for continuing the work later: the overall goal, key decisions made, files created or modified and how, and anything still unresolved. Skip narration and pleasantries — just the substance needed to pick back up."

// compact sends messages plus the compaction instruction and returns the
// model's summary.
func compact(client *agent.Client, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		history := append(append([]agent.Message{}, messages...), agent.Message{Role: "user", Content: compactPrompt})

		ch, err := client.SendStreaming(context.Background(), history)
		if err != nil {
			return compactResultMsg{err: err}
		}

		var final agent.Event
		for ev := range ch {
			if ev.Done {
				final = ev
			}
		}
		if final.Err != nil {
			return compactResultMsg{err: final.Err}
		}
		return compactResultMsg{summary: final.Message.Content, usage: final.Usage}
	}
}

// compactedHistory replaces the full conversation with a single message
// carrying the model's own summary of it, framed as background for
// whatever comes next.
func compactedHistory(summary string) []agent.Message {
	return []agent.Message{
		{Role: "user", Content: "Here is a summary of our conversation so far, before it was compacted to save context:\n\n" + summary + "\n\nContinue from here."},
	}
}

// contextWarnThreshold is a conservative, deliberately generic rule of
// thumb — chisel doesn't maintain a per-model context-window table (the
// OpenCode Go catalog changes, and getting a specific model's exact limit
// wrong would be worse than not claiming one at all), so this just flags
// "this is getting large" rather than "you are at N% of this model's
// limit".
const contextWarnThreshold = 100_000

// formatTokenCount renders a token count compactly for the status bar.
func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
