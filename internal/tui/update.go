package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 4 // input line + status bar + margin
		m.textInput.Width = msg.Width - 2
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case streamEventMsg:
		return m.handleStreamEvent(msg)

	case toolResultMsg:
		return m.handleToolResult(msg.result)

	case modelCheckResultMsg:
		return m.handleModelCheckResult(msg)

	case compactResultMsg:
		return m.handleCompactResult(msg)

	case sessionSaveErrorMsg:
		m.appendLine(errorStyle.Render("session save failed: " + msg.err.Error()))
		return m, nil

	case autoCommitResultMsg:
		if msg.err != nil {
			m.appendLine(errorStyle.Render("auto-commit failed: " + msg.err.Error()))
		} else if msg.sha != "" {
			m.appendLine(dimStyle.Render("→ committed " + msg.sha))
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		return m, tea.Quit
	}

	switch m.state {
	case stateAwaitingPermission:
		switch msg.String() {
		case "y", "Y", "enter":
			call := m.pendingUses[0]
			m.state = stateExecutingTool
			m.appendLine(dimStyle.Render("  → approved"))
			return m, executeTool(m.workDir, m.client.ModelName(), m.bash, m.mcp, call)
		case "n", "N", "esc":
			return m.handleToolResult(agent.ToolResult{
				ID:      m.pendingUses[0].ID,
				Content: "The user denied permission for this action.",
				IsError: true,
			})
		}
		return m, nil

	case stateInput:
		if msg.Type == tea.KeyEnter {
			return m.submit()
		}
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd

	default:
		// Busy (waiting on the model or a tool) — ignore keystrokes.
		return m, nil
	}
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	text := m.textInput.Value()
	if text == "" {
		return m, nil
	}
	m.textInput.Reset()

	if strings.HasPrefix(text, "/") {
		var cmd tea.Cmd
		m, cmd = m.handleCommand(text)
		return m, tea.Batch(cmd, textinput.Blink)
	}

	m.messages = append(m.messages, agent.Message{Role: "user", Content: text})
	m.appendLine(userStyle.Render("you  ") + text)
	m.state = stateWaitingModel

	return m, tea.Batch(startStream(m.client, m.messages), saveSession(m.workDir, m.messages), textinput.Blink)
}

// handleStreamEvent processes one event from the in-flight response. While
// the stream is still going it renders text deltas live and re-arms the
// listener; once done, it hands the fully accumulated message off to
// handleStreamComplete.
func (m Model) handleStreamEvent(msg streamEventMsg) (tea.Model, tea.Cmd) {
	ev := msg.event

	if ev.TextDelta != "" {
		m.appendStreamText(ev.TextDelta)
	}

	if !ev.Done {
		return m, waitForChunk(msg.ch)
	}

	if ev.Err != nil {
		m.err = ev.Err
		m.endStreamLine()
		m.appendLine(errorStyle.Render("error  " + ev.Err.Error()))
		m.state = stateInput
		return m, nil
	}

	return m.handleStreamComplete(*ev.Message, ev.FinishReason, ev.Usage)
}

func (m Model) handleStreamComplete(resp agent.Message, finishReason string, usage agent.Usage) (tea.Model, tea.Cmd) {
	m.endStreamLine()

	m.messages = append(m.messages, resp)
	m.tokensIn += usage.InputTokens
	m.tokensOut += usage.OutputTokens
	m.lastContextTokens = usage.InputTokens
	m.pendingUses = resp.ToolCalls
	save := saveSession(m.workDir, m.messages)

	if finishReason != "tool_calls" || len(m.pendingUses) == 0 {
		m.state = stateInput
		if m.autoCommit {
			return m, tea.Batch(save, autoCommit(m.workDir, lastUserText(m.messages)))
		}
		return m, save
	}

	next, cmd := m.dispatchNextTool()
	return next, tea.Batch(save, cmd)
}

// dispatchNextTool looks at the front of the pending tool-use queue and
// either asks for permission or runs it immediately.
func (m Model) dispatchNextTool() (tea.Model, tea.Cmd) {
	call := m.pendingUses[0]

	// Plan mode hard-denies anything that would otherwise need permission
	// — not just a prompt-level instruction the model might ignore. A
	// call that's already auto-allowed (glob, grep, editor view) is
	// read-only by definition and stays allowed; that's the whole point
	// of "read-only planning".
	if needsPermission(call) && m.client.PlanMode() {
		m.appendLine(errorStyle.Render("✗ blocked (plan mode): " + summarizeCall(call)))
		return m.handleToolResult(agent.ToolResult{
			ID:      call.ID,
			Content: "Not run — chisel is in plan mode, which only allows read-only exploration. Describe this as part of your plan instead, then stop; the user will exit plan mode before you make any changes.",
			IsError: true,
		})
	}

	if needsPermission(call) {
		m.state = stateAwaitingPermission
		prompt := fmt.Sprintf("allow %s?  [y/n]", summarizeCall(call))
		if diff, ok := agent.PreviewEdit(m.workDir, call); ok {
			prompt = fmt.Sprintf("allow %s?\n\n%s\n[y/n]", summarizeCall(call), strings.TrimRight(diff, "\n"))
		}
		m.appendLine(permissionStyle.Render(prompt))
		return m, nil
	}

	m.state = stateExecutingTool
	m.appendLine(toolStyle.Render("  " + summarizeCall(call)))
	return m, executeTool(m.workDir, m.client.ModelName(), m.bash, m.mcp, call)
}

func (m Model) handleToolResult(result agent.ToolResult) (tea.Model, tea.Cmd) {
	if len(m.pendingUses) == 0 {
		return m, nil
	}

	if result.IsError {
		m.appendLine(errorStyle.Render("  ✗ " + firstLine(result.Content)))
	} else {
		m.appendLine(dimStyle.Render("  ✓ " + firstLine(result.Content)))
	}

	m.pendingResults = append(m.pendingResults, result.ToMessage())
	m.pendingUses = m.pendingUses[1:]

	if len(m.pendingUses) > 0 {
		return m.dispatchNextTool()
	}

	m.messages = append(m.messages, m.pendingResults...)
	m.pendingResults = nil
	m.state = stateWaitingModel
	return m, tea.Batch(startStream(m.client, m.messages), saveSession(m.workDir, m.messages))
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
