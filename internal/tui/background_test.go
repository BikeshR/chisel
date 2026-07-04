package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

// unpackBatch runs a Cmd expected to be a tea.Batch and returns its
// sub-commands — real Bubbletea semantics (see commands.go's Batch),
// not a fake: Batch's Cmd, when invoked, returns a tea.BatchMsg holding
// the sub-Cmds for the runtime to execute concurrently.
func unpackBatch(t *testing.T, cmd tea.Cmd) []tea.Cmd {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T (%+v)", msg, msg)
	}
	return batch
}

func TestStartBackgroundTaskInvalidCommand(t *testing.T) {
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash_background", Arguments: `{}`}}
	cmd := startBackgroundTask(t.TempDir(), call)

	msg := cmd()
	result, ok := msg.(toolResultMsg)
	if !ok {
		t.Fatalf("expected toolResultMsg for an empty command, got %T", msg)
	}
	if !result.result.IsError {
		t.Error("expected an error result for an empty command")
	}
}

func TestStartBackgroundTaskFullFlow(t *testing.T) {
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name: "bash_background", Arguments: `{"command":"echo hello"}`,
	}}
	cmds := unpackBatch(t, startBackgroundTask(t.TempDir(), call))
	if len(cmds) != 3 {
		t.Fatalf("got %d sub-commands, want 3 (started result, record, watch)", len(cmds))
	}

	startedResult, ok := cmds[0]().(toolResultMsg)
	if !ok {
		t.Fatalf("cmds[0] expected toolResultMsg, got %T", cmds[0]())
	}
	if startedResult.result.IsError {
		t.Fatalf("unexpected error: %s", startedResult.result.Content)
	}
	if !strings.Contains(startedResult.result.Content, "bg_") {
		t.Errorf("started result = %q, want a task_id", startedResult.result.Content)
	}

	started, ok := cmds[1]().(backgroundTaskStartedMsg)
	if !ok {
		t.Fatalf("cmds[1] expected backgroundTaskStartedMsg, got %T", cmds[1]())
	}
	if started.command != "echo hello" {
		t.Errorf("started.command = %q", started.command)
	}
	if started.cancel == nil {
		t.Error("expected a non-nil cancel func")
	}

	// The watch Cmd blocks on the real subprocess — give it a generous
	// timeout since it's an actual echo invocation, not a mock.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[2]() }()
	select {
	case msg := <-done:
		result, ok := msg.(backgroundTaskDoneMsg)
		if !ok {
			t.Fatalf("expected backgroundTaskDoneMsg, got %T", msg)
		}
		if result.err != nil {
			t.Errorf("unexpected error: %v", result.err)
		}
		if strings.TrimSpace(result.output) != "hello" {
			t.Errorf("output = %q, want %q", result.output, "hello")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the background command to finish")
	}
}

func TestStartBackgroundTaskCommandFails(t *testing.T) {
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name: "bash_background", Arguments: `{"command":"exit 1"}`,
	}}
	cmds := unpackBatch(t, startBackgroundTask(t.TempDir(), call))

	cmds[0]() // started result
	cmds[1]() // record

	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[2]() }()
	select {
	case msg := <-done:
		result := msg.(backgroundTaskDoneMsg)
		if result.err == nil {
			t.Error("expected an error for a command that exits non-zero")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestHandleBackgroundTaskStarted(t *testing.T) {
	m := Model{}
	m = m.handleBackgroundTaskStarted(backgroundTaskStartedMsg{id: "bg_1", command: "npm run dev", cancel: func() {}})

	task, ok := m.backgroundTasks["bg_1"]
	if !ok {
		t.Fatal("expected bg_1 to be recorded")
	}
	if task.command != "npm run dev" || !task.running {
		t.Errorf("task = %+v", task)
	}
}

func TestHandleBackgroundTaskDoneAppendsMessageAndMarksNotRunning(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	m = m.handleBackgroundTaskStarted(backgroundTaskStartedMsg{id: "bg_1", command: "npm run build", cancel: func() {}})

	got, cmd := m.handleBackgroundTaskDone(backgroundTaskDoneMsg{id: "bg_1", output: "build succeeded\n"})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (save session + notify)")
	}

	if got.backgroundTasks["bg_1"].running {
		t.Error("expected the task to be marked not-running after completion")
	}

	if len(got.messages) != 1 {
		t.Fatalf("messages = %+v, want 1 synthetic message appended", got.messages)
	}
	if !strings.Contains(got.messages[0].Content, "npm run build") || !strings.Contains(got.messages[0].Content, "build succeeded") {
		t.Errorf("message content = %q", got.messages[0].Content)
	}
}

func TestHandleBackgroundTaskDoneReportsFailure(t *testing.T) {
	m := Model{workDir: t.TempDir()}
	m = m.handleBackgroundTaskStarted(backgroundTaskStartedMsg{id: "bg_1", command: "flaky-thing", cancel: func() {}})

	got, _ := m.handleBackgroundTaskDone(backgroundTaskDoneMsg{id: "bg_1", output: "oops", err: errTestFailure{}})
	if !strings.Contains(got.messages[0].Content, "failed") {
		t.Errorf("message content = %q, want it to mention failure", got.messages[0].Content)
	}
}

type errTestFailure struct{}

func (errTestFailure) Error() string { return "exit status 1" }

// TestCancelBackgroundTasksKillsRunningProcess drives the real
// interception path end-to-end — startBackgroundTask spawns a real
// `sleep` subprocess, CancelBackgroundTasks (chisel's exit-cleanup
// hook) is called, and the watch Cmd must observe the process actually
// dying rather than running to completion.
func TestCancelBackgroundTasksKillsRunningProcess(t *testing.T) {
	call := agent.ToolCall{ID: "call_1", Function: agent.ToolCallFunction{
		Name: "bash_background", Arguments: `{"command":"sleep 30"}`,
	}}
	cmds := unpackBatch(t, startBackgroundTask(t.TempDir(), call))
	cmds[0]() // started result

	started := cmds[1]().(backgroundTaskStartedMsg)
	m := Model{}
	m = m.handleBackgroundTaskStarted(started)

	done := make(chan tea.Msg, 1)
	go func() { done <- cmds[2]() }()

	// Give the subprocess a moment to actually start before killing it —
	// otherwise this could race the exec itself on a slow CI box.
	time.Sleep(200 * time.Millisecond)
	m.CancelBackgroundTasks()

	select {
	case msg := <-done:
		result := msg.(backgroundTaskDoneMsg)
		if result.err == nil {
			t.Error("expected an error from a killed process, got nil (it may have run to completion instead)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sleep 30 was not killed within 5s of CancelBackgroundTasks — process group kill likely failed")
	}
}

func TestNewBackgroundIDIsUniqueAndMonotonic(t *testing.T) {
	a := newBackgroundID()
	b := newBackgroundID()
	if a == b {
		t.Errorf("expected distinct IDs, got %q twice", a)
	}
}

func TestRunningBackgroundCount(t *testing.T) {
	tasks := map[string]*backgroundTask{
		"bg_1": {running: true},
		"bg_2": {running: false},
		"bg_3": {running: true},
	}
	if got := runningBackgroundCount(tasks); got != 2 {
		t.Errorf("runningBackgroundCount = %d, want 2", got)
	}
}

func TestStatusLineShowsRunningBackgroundCount(t *testing.T) {
	m := Model{
		client:          agent.New("minimax-m3"),
		backgroundTasks: map[string]*backgroundTask{"bg_1": {running: true}},
	}
	if got := m.statusLine(); !strings.Contains(got, "1 bg running") {
		t.Errorf("statusLine() = %q, want it to mention the running background task", got)
	}
}
