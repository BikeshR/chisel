package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// backgroundTask tracks one command started via bash_background.
// running/command are only ever read or written from Update (the
// single-threaded Bubbletea event loop) — the goroutine actually
// running the command never touches this struct, only a channel it
// sends its result to exactly once (see startBackgroundTask), the same
// "capture locals, hand off through a channel rather than shared state"
// pattern bashsession.go and mcp.Server already use for their own
// background work.
type backgroundTask struct {
	command string
	cancel  context.CancelFunc
	running bool
}

// backgroundTaskStartedMsg records a newly-started background task —
// separate from the tool result the model sees (see startBackgroundTask),
// since Update needs to record it in Model.backgroundTasks regardless of
// whatever the model does with its own "started" response.
type backgroundTaskStartedMsg struct {
	id      string
	command string
	cancel  context.CancelFunc
}

// backgroundTaskDoneMsg carries a finished background task's result,
// whenever that is — potentially many turns after the one that started it.
type backgroundTaskDoneMsg struct {
	id     string
	output string
	err    error
}

// nextBackgroundID is process-wide and monotonic rather than a Model
// field: tool dispatch is strictly sequential (see CLAUDE.md), but the
// Cmd that generates an ID runs on its own goroutine, off the Update
// goroutine that owns Model's value-semantics state — a plain field
// read-then-incremented there wouldn't be safe the way this is.
var nextBackgroundID int64

func newBackgroundID() string {
	return fmt.Sprintf("bg_%d", atomic.AddInt64(&nextBackgroundID, 1))
}

// startBackgroundTask handles the bash_background tool specially,
// intercepted in executeTool before it would otherwise reach
// agent.Execute — the same interception point MCP calls already use
// there. A background task needs to outlive the single tool-call
// round-trip entirely, which Execute's synchronous "run it, return the
// result" contract can't express.
//
// Deliberately runs as a standalone process, not through the persistent
// BashSession: that session's single-owner-access contract (documented
// in bashsession.go) would be violated by a second, concurrent caller.
// Setpgid plus a Cancel override that kills the whole process group
// (not just the direct sh process) mirrors BashSession.stop's own
// cleanup, so a background command that itself spawns children doesn't
// orphan them when cancelled.
//
// Returns a Cmd that fires three things at once: the "started" tool
// result (so the model's turn continues normally without waiting), a
// record of the new task for Update to add to Model.backgroundTasks,
// and a watcher that blocks until the command actually finishes and
// reports back via backgroundTaskDoneMsg — independent of whatever turn
// or state chisel is in by the time that happens.
func startBackgroundTask(workDir string, call agent.ToolCall) tea.Cmd {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil || in.Command == "" {
		return func() tea.Msg {
			return toolResultMsg{result: agent.ToolResult{
				ID: call.ID, Content: "bash_background requires a non-empty command", IsError: true,
			}}
		}
	}

	id := newBackgroundID()
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	resultCh := make(chan backgroundTaskDoneMsg, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		resultCh <- backgroundTaskDoneMsg{id: id, output: string(out), err: err}
	}()

	startedResult := func() tea.Msg {
		content := fmt.Sprintf(
			`{"task_id":%q,"status":"started — its output will be added to the conversation, and the user notified, once it finishes"}`, id)
		return toolResultMsg{result: agent.ToolResult{ID: call.ID, Content: content}}
	}
	recordStarted := func() tea.Msg {
		return backgroundTaskStartedMsg{id: id, command: in.Command, cancel: cancel}
	}
	watch := func() tea.Msg {
		return <-resultCh
	}

	return tea.Batch(startedResult, recordStarted, watch)
}

// handleBackgroundTaskStarted records a task that just started — see
// startBackgroundTask.
func (m Model) handleBackgroundTaskStarted(msg backgroundTaskStartedMsg) Model {
	if m.backgroundTasks == nil {
		m.backgroundTasks = map[string]*backgroundTask{}
	}
	m.backgroundTasks[msg.id] = &backgroundTask{command: msg.command, cancel: msg.cancel, running: true}
	return m
}

// handleBackgroundTaskDone folds a finished background task's output
// into the conversation (as a synthetic user-role message — there's no
// preceding tool_call for it to pair with, unlike a normal tool result)
// so the model picks it up on its next turn without needing a separate
// "check on it" tool, and notifies the user (bell/OSC9) the same way a
// permission prompt or turn completion does, since this is exactly the
// same "chisel needs your attention" moment, just on its own schedule.
func (m Model) handleBackgroundTaskDone(msg backgroundTaskDoneMsg) (Model, tea.Cmd) {
	command := msg.id
	if task, ok := m.backgroundTasks[msg.id]; ok {
		task.running = false
		command = task.command
	}

	status := "finished"
	if msg.err != nil {
		status = "failed: " + msg.err.Error()
	}
	summary := fmt.Sprintf("background task %s (%q) %s", msg.id, command, status)
	m.appendLine(dimStyle.Render("── " + summary + " ──"))

	content := fmt.Sprintf("[%s]\n\n%s", summary, agent.TruncateOutput(msg.output))
	m.messages = append(m.messages, agent.Message{Role: "user", Content: content})

	return m, tea.Batch(saveSession(m.workDir, m.messages), notifyIdle(summary))
}

// CancelBackgroundTasks kills every still-running background task's
// process group — called once, on chisel exit (main.go), so a
// long-running background command doesn't outlive the session that
// started it and become an orphaned process.
func (m Model) CancelBackgroundTasks() {
	for _, t := range m.backgroundTasks {
		if t.running && t.cancel != nil {
			t.cancel()
		}
	}
}
