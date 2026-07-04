package tui

import (
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/hooks"
	"github.com/BikeshR/chisel/internal/mcp"
)

func TestHandleHelpCommandListsCommandsAndKeys(t *testing.T) {
	m := Model{}
	got := m.handleHelpCommand()
	lines := got.renderedLines()
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (help is one multi-line entry)", len(lines))
	}
	for _, want := range []string{"/model", "/compact", "/retry", "/status", "alt+enter", "ctrl+c"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("help text missing %q", want)
		}
	}
}

func TestUnknownCommandMentionsHelp(t *testing.T) {
	m := Model{}
	got, _ := m.handleCommand("/bogus")
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "/help") {
		t.Errorf("lines = %+v, want the unknown-command error to mention /help", lines)
	}
}

func TestHandleRetryCommandEmptyHistory(t *testing.T) {
	m := Model{}
	got, cmd := m.handleRetryCommand()
	if cmd != nil {
		t.Error("expected a nil Cmd when there's nothing to retry")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "nothing to retry") {
		t.Errorf("lines = %+v", lines)
	}
}

func TestHandleRetryCommandResendsHistoryWithoutAddingAMessage(t *testing.T) {
	m := Model{
		client:   agent.New("minimax-m3"),
		messages: []agent.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
	}
	before := len(m.messages)

	gotModel, cmd := m.handleRetryCommand()
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to re-send the history")
	}
	if gotModel.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", gotModel.state)
	}
	if len(gotModel.messages) != before {
		t.Errorf("messages count = %d, want unchanged at %d — retry doesn't add a new message", len(gotModel.messages), before)
	}
}

func TestHandleStatusCommandReportsWorkdirHooksAndMemory(t *testing.T) {
	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.PreToolUse = []hooks.Hook{{Match: "*", Command: "exit 0"}}

	m := Model{
		workDir:    "/some/project",
		hooks:      hooksCfg,
		memUser:    true,
		memProject: true,
	}
	got := m.handleStatusCommand()
	lines := got.renderedLines()

	joined := strings.Join(lines, "\n")
	for _, want := range []string{"/some/project", "1 preToolUse", "CHISEL.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("status output missing %q: %+v", want, lines)
		}
	}
}

func TestHandleStatusCommandReportsNoHooksOrMemoryWhenAbsent(t *testing.T) {
	m := Model{workDir: "/some/project"}
	got := m.handleStatusCommand()
	joined := strings.Join(got.renderedLines(), "\n")

	if !strings.Contains(joined, "hooks: none") {
		t.Errorf("status output = %q, want it to report no hooks configured", joined)
	}
	if !strings.Contains(joined, "memory: none loaded") {
		t.Errorf("status output = %q, want it to report no memory loaded", joined)
	}
}

func TestBrokenMCPCountCountsOnlyBroken(t *testing.T) {
	statuses := []mcp.ServerStatus{
		{Name: "ok-server", Broken: false},
		{Name: "dead-server", Broken: true},
		{Name: "another-dead", Broken: true},
	}
	if got := brokenMCPCount(statuses); got != 2 {
		t.Errorf("brokenMCPCount = %d, want 2", got)
	}
}

func TestBrokenMCPCountZeroForNoServers(t *testing.T) {
	if got := brokenMCPCount(nil); got != 0 {
		t.Errorf("brokenMCPCount(nil) = %d, want 0", got)
	}
}

// TestSyncMCPHealthWithNilRegistryDoesNotPanic covers the common
// test-construction case (a bare Model{} with no real MCP registry) —
// syncMCPHealth is called unconditionally at the start of every turn,
// so it must be safe even when nothing is configured.
func TestSyncMCPHealthWithNilRegistryDoesNotPanic(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	m.syncMCPHealth()
}

func TestCustomCommandExpandsAndSubmits(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		customCommands: map[string]customcmd.Command{
			"review": {Name: "review", Template: "review $ARGUMENTS for bugs"},
		},
	}

	got, cmd := m.handleCommand("/review main.go")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to start the request")
	}
	if len(got.messages) != 1 {
		t.Fatalf("messages = %+v, want the expanded template sent", got.messages)
	}
	if got.messages[0].Content != "review main.go for bugs" {
		t.Errorf("message content = %q, want the expanded template", got.messages[0].Content)
	}
	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel", got.state)
	}
}

func TestCustomCommandWithoutArguments(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		customCommands: map[string]customcmd.Command{
			"standup": {Name: "standup", Template: "summarize what changed today"},
		},
	}

	got, cmd := m.handleCommand("/standup")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	if len(got.messages) != 1 || got.messages[0].Content != "summarize what changed today" {
		t.Errorf("messages = %+v", got.messages)
	}
}

func TestUnknownCommandStillReportsErrorWhenNoCustomMatch(t *testing.T) {
	m := Model{customCommands: map[string]customcmd.Command{"review": {}}}
	got, cmd := m.handleCommand("/bogus")
	if cmd != nil {
		t.Error("expected a nil Cmd for an unknown command")
	}
	lines := got.renderedLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "unknown command") {
		t.Errorf("lines = %+v, want an unknown-command error", lines)
	}
}

func TestHelpListsCustomCommands(t *testing.T) {
	m := Model{
		customCommands: map[string]customcmd.Command{
			"review": {Name: "review"},
			"deploy": {Name: "deploy"},
		},
	}
	got := m.handleHelpCommand()
	lines := got.renderedLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "/review") || !strings.Contains(joined, "/deploy") {
		t.Errorf("help output = %q, want both custom commands listed", joined)
	}
}
