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
	width, height       int
	err                 error
	quitting            bool
}

// New builds the initial Model for a chisel session rooted at workDir.
// bash and mcpRegistry are owned by the caller (main.go), not created
// here, so their lifecycle (closing the shell / MCP server processes on
// exit) doesn't depend on anything inside this package. resumed and
// savedAt come from session.Load — pass a nil/zero pair if there's
// nothing to resume.
func New(client *agent.Client, workDir string, bash *agent.BashSession, mcpRegistry *mcp.Registry, resumed []agent.Message, savedAt time.Time) Model {
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
		messages:      resumed,
		textInput:     ti,
		viewport:      viewport.New(80, 20),
		spinner:       sp,
		state:         stateInput,
		streamLineIdx: -1,
	}

	if len(resumed) > 0 {
		m.lines = append(m.lines, resumeBanner(len(resumed), savedAt))
		m.lines = append(m.lines, renderHistory(resumed, m.showThinking)...)
		m.viewport.SetContent(joinLines(m.lines))
		m.viewport.GotoBottom()
	}

	return m
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

func executeTool(workDir string, bash *agent.BashSession, mcpRegistry *mcp.Registry, call agent.ToolCall) tea.Cmd {
	return func() tea.Msg {
		if mcp.IsToolName(call.Function.Name) {
			args := json.RawMessage(call.Function.Arguments)
			content, isError, err := mcpRegistry.Call(context.Background(), call.Function.Name, args)
			if err != nil {
				return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: err.Error(), IsError: true}}
			}
			return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: content, IsError: isError}}
		}
		return toolResultMsg{result: agent.Execute(context.Background(), workDir, call, bash)}
	}
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
