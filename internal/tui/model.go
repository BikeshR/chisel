package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
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

	tokensIn, tokensOut int64
	width, height       int
	err                 error
	quitting            bool
}

// New builds the initial Model for a chisel session rooted at workDir.
// bash is owned by the caller (main.go), not created here, so its
// lifecycle (in particular, closing the underlying shell on exit) doesn't
// depend on anything inside this package.
func New(client *agent.Client, workDir string, bash *agent.BashSession) Model {
	ti := textinput.New()
	ti.Placeholder = "ask chisel to do something…"
	ti.Focus()
	ti.CharLimit = 4000

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))

	return Model{
		client:        client,
		workDir:       workDir,
		bash:          bash,
		textInput:     ti,
		viewport:      viewport.New(80, 20),
		spinner:       sp,
		state:         stateInput,
		streamLineIdx: -1,
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

func executeTool(workDir string, bash *agent.BashSession, call agent.ToolCall) tea.Cmd {
	return func() tea.Msg {
		return toolResultMsg{result: agent.Execute(context.Background(), workDir, call, bash)}
	}
}
