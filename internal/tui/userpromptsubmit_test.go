package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/customcmd"
	"github.com/BikeshR/chisel/internal/hooks"
)

var errHookBroken = errors.New("hook broke")

// TestDispatchTextRunsUserPromptSubmitHookAsync confirms a plain
// message goes through the hook-check path (an async Cmd, not a
// synchronous decision) whenever any UserPromptSubmit hooks are
// configured — mirroring how preToolUse hooks are already required to
// run asynchronously (see executeTool's own doc comment).
func TestDispatchTextRunsUserPromptSubmitHookAsync(t *testing.T) {
	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.UserPromptSubmit = []hooks.Hook{{Command: "exit 0"}}

	m := Model{client: agent.New("minimax-m3"), hooks: hooksCfg}
	got, cmd := m.dispatchText("hello")

	if got.state != stateWaitingModel {
		t.Errorf("state = %v, want stateWaitingModel while the hook check is pending", got.state)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to run the hook check")
	}
	// The message must not be appended to history yet — it isn't
	// cleared to send until the hook result comes back.
	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want empty until the hook result arrives", got.messages)
	}

	msg, ok := cmd().(userPromptHookResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want userPromptHookResultMsg", cmd())
	}
	if msg.blocked {
		t.Error("blocked = true, want false for an exit-0 hook")
	}
}

// TestDispatchTextSkipsHookCheckWithNoHooksConfigured confirms no
// extra round trip is introduced when there's nothing to check —
// dispatchText should submit directly, the same as before this feature
// existed.
func TestDispatchTextSkipsHookCheckWithNoHooksConfigured(t *testing.T) {
	m := Model{client: agent.New("minimax-m3")}
	got, cmd := m.dispatchText("hello")

	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	if len(got.messages) != 1 || got.messages[0].Content != "hello" {
		t.Errorf("messages = %+v, want the message submitted directly", got.messages)
	}
}

func TestHandleUserPromptHookResultBlockedRevertsToInput(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, _ := m.handleUserPromptHookResult(userPromptHookResultMsg{
		text:    "here is my api key: xyz",
		blocked: true,
		reason:  "message contains a secret",
	})

	if got.state != stateInput {
		t.Errorf("state = %v, want stateInput after a block", got.state)
	}
	if len(got.messages) != 0 {
		t.Errorf("messages = %+v, want the message never actually sent", got.messages)
	}
	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "message contains a secret") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want a line explaining the block", lines)
	}
}

func TestHandleUserPromptHookResultAllowedSubmits(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, cmd := m.handleUserPromptHookResult(userPromptHookResultMsg{text: "hello", blocked: false})

	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to actually start the request")
	}
	if len(got.messages) != 1 || got.messages[0].Content != "hello" {
		t.Errorf("messages = %+v, want the message submitted", got.messages)
	}
}

// TestHandleUserPromptHookResultFailsOpenOnHookError confirms a broken
// hook (a real error, not a judged block) doesn't silently swallow the
// user's message — the hook itself being broken shouldn't be
// indistinguishable from the message being unsafe.
func TestHandleUserPromptHookResultFailsOpenOnHookError(t *testing.T) {
	m := Model{client: agent.New("minimax-m3"), state: stateWaitingModel}
	got, cmd := m.handleUserPromptHookResult(userPromptHookResultMsg{
		text: "hello",
		err:  errHookBroken,
	})

	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — a hook error should fail open, not silently drop the message")
	}
	if len(got.messages) != 1 || got.messages[0].Content != "hello" {
		t.Errorf("messages = %+v, want the message still submitted despite the hook error", got.messages)
	}
	lines := got.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, errHookBroken.Error()) {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the hook error surfaced", lines)
	}
}

// TestCustomCommandExpansionGoesThroughHookCheck confirms a custom
// command's expanded text is also subject to UserPromptSubmit hooks,
// not just a directly-typed message — otherwise a hook meant to gate
// what reaches the model could be trivially bypassed via a custom
// command instead.
func TestCustomCommandExpansionGoesThroughHookCheck(t *testing.T) {
	hooksCfg := hooks.Config{}
	hooksCfg.Hooks.UserPromptSubmit = []hooks.Hook{{Command: "exit 1"}}

	m := Model{
		client: agent.New("minimax-m3"),
		hooks:  hooksCfg,
		customCommands: map[string]customcmd.Command{
			"review": {Name: "review", Template: "review this for bugs"},
		},
	}
	_, cmd := m.handleCommand("/review")
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd to run the hook check")
	}
	msg, ok := cmd().(userPromptHookResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want userPromptHookResultMsg", cmd())
	}
	if !msg.blocked {
		t.Error("blocked = false, want true — an exit-1 UserPromptSubmit hook should block a custom command's expanded text too")
	}
}
