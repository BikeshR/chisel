package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/permrules"
)

func TestDecidePermissionAllowsReadOnlyTools(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}}
	decision, _ := decidePermission(call, false, nil, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for a read-only tool", decision)
	}
}

func TestDecidePermissionAsksForBash(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	decision, _ := decidePermission(call, false, nil, nil)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk for bash", decision)
	}
}

func TestDecidePermissionDeniesInPlanMode(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	decision, reason := decidePermission(call, true, nil, nil)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny in plan mode", decision)
	}
	if !strings.Contains(reason, "plan mode") {
		t.Errorf("reason = %q, want it to mention plan mode", reason)
	}
}

func TestDecidePermissionPlanModeOverridesAllowlist(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	allowlist := map[string]bool{"bash:ls": true}
	decision, _ := decidePermission(call, true, allowlist, nil)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — plan mode must override even an allowlisted command", decision)
	}
}

func TestDecidePermissionAllowsAllowlistedBashCommand(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./..."}`}}
	allowlist := map[string]bool{"bash:go test ./...": true}
	decision, _ := decidePermission(call, false, allowlist, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for an allowlisted command", decision)
	}
}

func TestDecidePermissionAllowlistIsExactCommandMatch(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./... -v"}`}}
	allowlist := map[string]bool{"bash:go test ./...": true}
	decision, _ := decidePermission(call, false, allowlist, nil)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — a different command string must not match", decision)
	}
}

func TestAllowlistKeyExcludesEdits(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"a.go","old_str":"x","new_str":"y"}`,
	}}
	if _, ok := allowlistKey(call); ok {
		t.Error("expected edits to not support allowlisting — each edit is materially different")
	}
}

func TestAllowlistKeyForBashBackground(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash_background", Arguments: `{"command":"npm run dev"}`}}
	key, ok := allowlistKey(call)
	if !ok {
		t.Fatal("expected bash_background to support allowlisting")
	}
	if key != "bash_background:npm run dev" {
		t.Errorf("key = %q", key)
	}
}

func TestAllowlistKeyForMCPTool(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "mcp__github__create_issue", Arguments: `{}`}}
	key, ok := allowlistKey(call)
	if !ok {
		t.Fatal("expected MCP tools to support allowlisting")
	}
	if !strings.Contains(key, "mcp__github__create_issue") {
		t.Errorf("key = %q, want it to identify the specific MCP tool", key)
	}
}

func TestMCPCallArgsPreviewShowsPrettyPrintedArgs(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "mcp__github__create_issue",
		Arguments: `{"title":"bug report","body":"details here"}`,
	}}
	preview := mcpCallArgsPreview(call)
	if !strings.Contains(preview, "bug report") || !strings.Contains(preview, "details here") {
		t.Errorf("preview = %q, want it to show the call's arguments", preview)
	}
}

func TestMCPCallArgsPreviewEmptyForNonMCPCall(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	if got := mcpCallArgsPreview(call); got != "" {
		t.Errorf("preview = %q, want empty for a non-MCP call", got)
	}
}

func TestPersistableRuleForBash(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./..."}`}}
	toolName, pattern, ok := persistableRuleFor(call)
	if !ok || toolName != "bash" || pattern != "go test ./..." {
		t.Errorf("got (%q, %q, %v), want (\"bash\", \"go test ./...\", true)", toolName, pattern, ok)
	}
}

func TestPersistableRuleForExcludesEditsAndMCP(t *testing.T) {
	cases := []agent.ToolCall{
		{Function: agent.ToolCallFunction{Name: "str_replace_based_edit_tool", Arguments: `{"command":"str_replace","path":"a.go","old_str":"x","new_str":"y"}`}},
		{Function: agent.ToolCallFunction{Name: "mcp__github__create_issue", Arguments: `{}`}},
		{Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}},
	}
	for _, call := range cases {
		if _, _, ok := persistableRuleFor(call); ok {
			t.Errorf("expected %s to not support a persistent rule", call.Function.Name)
		}
	}
}

func TestPressingPWritesPermanentRuleAndRuns(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: workDir,
		state:   stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"npm test"}`}},
		},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Runes: []rune("p"), Type: tea.KeyRunes})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — 'p' should run the call like 'y' does")
	}
	gotModel := got.(Model)
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool", gotModel.state)
	}

	if decision, matched := permrules.Match(gotModel.permRules, "bash", "npm test"); !matched || decision != permrules.Allow {
		t.Errorf("in-memory permRules Match = (%v, %v), want (Allow, true)", decision, matched)
	}

	loaded, found, err := permrules.Load(workDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("expected permissions.json to have been written")
	}
	if decision, matched := permrules.Match(loaded, "bash", "npm test"); !matched || decision != permrules.Allow {
		t.Errorf("Match after reload = (%v, %v), want (Allow, true)", decision, matched)
	}

	data, err := os.ReadFile(permrules.ConfigPath(workDir))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := permrules.IsTrusted(permrules.ContentHash(data))
	if err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Error("expected the newly written permissions.json to be auto-trusted")
	}
}

// TestPressingPDoesNotClobberExistingRulesWhenInMemoryStateIsStale is the
// regression test for a real destructive bug: the "p" handler used to
// build the new rule set on top of m.permRules directly. If that was nil
// (trust declined at startup, or the file parse-errored then) or simply
// stale (the file was written to disk after this session already
// loaded/cached it), Save would overwrite the real file with only the
// one new rule — silently destroying an existing repo-provided policy,
// then auto-trusting the result.
func TestPressingPDoesNotClobberExistingRulesWhenInMemoryStateIsStale(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	// A rule already exists on disk that this session's in-memory
	// m.permRules doesn't know about — e.g. trust was declined at
	// startup, so main.go set permRules = nil, or the file changed since.
	existing := permrules.Add(nil, "bash", "git push --force*", permrules.Deny)
	if err := permrules.Save(workDir, existing); err != nil {
		t.Fatal(err)
	}

	m := Model{
		client:    agent.New("minimax-m3"),
		workDir:   workDir,
		state:     stateAwaitingPermission,
		permRules: nil, // stale/declined in-memory state — the file on disk has more than this
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"npm test"}`}},
		},
	}

	if _, cmd := m.handleKey(tea.KeyMsg{Runes: []rune("p"), Type: tea.KeyRunes}); cmd == nil {
		t.Fatal("expected a non-nil Cmd — 'p' should run the call")
	}

	loaded, found, err := permrules.Load(workDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("expected permissions.json to still exist")
	}

	// The pre-existing deny rule must have survived.
	if decision, matched := permrules.Match(loaded, "bash", "git push --force origin main"); !matched || decision != permrules.Deny {
		t.Errorf("pre-existing rule Match = (%v, %v), want (Deny, true) — it must not be clobbered", decision, matched)
	}
	// The newly added rule must also be present.
	if decision, matched := permrules.Match(loaded, "bash", "npm test"); !matched || decision != permrules.Allow {
		t.Errorf("new rule Match = (%v, %v), want (Allow, true)", decision, matched)
	}
}

func TestPressingPOnIneligibleCallStillApproves(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		state:  stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "str_replace_based_edit_tool", Arguments: `{"command":"create","path":"a.go","file_text":"x"}`}},
		},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Runes: []rune("p"), Type: tea.KeyRunes})
	if cmd == nil {
		t.Fatal("expected 'p' to still approve and run an ineligible call")
	}
	gotModel := got.(Model)
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool", gotModel.state)
	}
}

func TestPressingAAddsToAllowlistAndRuns(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		state:  stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./..."}`}},
		},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Runes: []rune("a"), Type: tea.KeyRunes})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — 'a' should run the call like 'y' does")
	}
	gotModel := got.(Model)
	if gotModel.state != stateExecutingTool {
		t.Errorf("state = %v, want stateExecutingTool", gotModel.state)
	}
	if !gotModel.sessionAllowlist["bash:go test ./..."] {
		t.Error("expected the exact command to be added to the session allowlist")
	}
}

func TestPressingEnterDoesNotApprove(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		state:  stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		},
	}

	got, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(Model)
	if gotModel.state != stateAwaitingPermission {
		t.Errorf("state = %v, want unchanged stateAwaitingPermission — enter must not approve", gotModel.state)
	}
}

func TestPressingNQueuesADenialReasonInsteadOfRespondingImmediately(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		state:  stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Runes: []rune("n"), Type: tea.KeyRunes})
	if cmd != nil {
		t.Error("expected a nil Cmd — denying should wait for the user's reason, not immediately resend to the model")
	}
	gotModel := got.(Model)
	if gotModel.state != stateInput {
		t.Errorf("state = %v, want stateInput", gotModel.state)
	}
	if !gotModel.awaitingDenialReason {
		t.Error("expected awaitingDenialReason to be set")
	}
	if len(gotModel.pendingUses) != 1 {
		t.Error("pendingUses shouldn't be resolved yet — that happens once the reason is submitted")
	}
}

func TestSubmittingAfterDenialResolvesWithTheTypedReason(t *testing.T) {
	m := newInputModel()
	m.awaitingDenialReason = true
	m.pendingUses = []agent.ToolCall{
		{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
	}
	m.textArea.SetValue("use git rm instead")

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — resolving the denial should invoke the model again")
	}
	gotModel := got.(Model)
	if gotModel.awaitingDenialReason {
		t.Error("awaitingDenialReason should be cleared after being resolved")
	}
	if len(gotModel.pendingUses) != 0 {
		t.Error("the denied call should be resolved (pendingUses cleared)")
	}
	found := false
	for _, msg := range gotModel.messages {
		if strings.Contains(msg.Content, "use git rm instead") {
			found = true
		}
	}
	if !found {
		t.Errorf("messages = %+v, want the typed reason included in the tool result", gotModel.messages)
	}
}

func TestSubmittingEmptyAfterDenialUsesGenericReason(t *testing.T) {
	m := newInputModel()
	m.awaitingDenialReason = true
	m.pendingUses = []agent.ToolCall{
		{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	gotModel := got.(Model)
	found := false
	for _, msg := range gotModel.messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "denied permission") {
			found = true
		}
	}
	if !found {
		t.Errorf("messages = %+v, want a generic denial reason", gotModel.messages)
	}
}

func TestEscStillDeniesImmediatelyWithoutAskingForAReason(t *testing.T) {
	m := Model{
		client: agent.New("minimax-m3"),
		state:  stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		},
	}

	got, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd — esc still resolves the call immediately")
	}
	gotModel := got.(Model)
	if len(gotModel.pendingUses) != 0 {
		t.Error("expected esc to resolve the pending call right away")
	}
	if gotModel.awaitingDenialReason {
		t.Error("esc shouldn't queue a denial reason prompt — that's 'n' specifically")
	}
}

func TestPermissionPromptShowsCwdWhenDriftedFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	bash := agent.NewBashSession(workDir)
	defer bash.Close()
	if _, err := bash.Run(context.Background(), "cd /tmp", false); err != nil {
		t.Fatalf("cd: %v", err)
	}

	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: workDir,
		bash:    bash,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}},
		},
	}

	got, _ := m.dispatchNextTool()
	gotModel := got.(Model)
	lines := gotModel.renderedLines()
	found := false
	for _, l := range lines {
		if strings.Contains(l, "/tmp") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %+v, want the prompt to mention the shell's drifted cwd (/tmp)", lines)
	}
}

func TestPermissionPromptOmitsCwdWhenUnchanged(t *testing.T) {
	workDir := t.TempDir()
	bash := agent.NewBashSession(workDir)
	defer bash.Close()
	if _, err := bash.Run(context.Background(), "echo hi", false); err != nil {
		t.Fatalf("echo: %v", err)
	}

	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: workDir,
		bash:    bash,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}},
		},
	}

	got, _ := m.dispatchNextTool()
	gotModel := got.(Model)
	lines := gotModel.renderedLines()
	for _, l := range lines {
		if strings.Contains(l, "(in ") {
			t.Errorf("lines = %+v, want no cwd hint when the shell never left workDir", lines)
		}
	}
}

func TestDecidePermissionRuleAllowsBashCommand(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"git status"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "git *", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, false, nil, rules)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow — a matching allow rule", decision)
	}
}

func TestDecidePermissionRuleDeniesBashCommand(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /tmp/x"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "rm -rf *", Decision: permrules.Deny}}}

	decision, reason := decidePermission(call, false, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — a matching deny rule", decision)
	}
	if !strings.Contains(reason, "permissions.json") {
		t.Errorf("reason = %q, want it to mention permissions.json", reason)
	}
}

// TestDecidePermissionDenyRuleOverridesEvenAnAutoAllowedTool: a deny
// rule making something *more* restrictive should apply regardless of
// what the call would otherwise need — chisel doesn't currently offer
// rules for auto-allowed tools like glob, but the precedence itself
// (deny checked before the normal needsPermission path) must hold for
// any tool name a rule names.
func TestDecidePermissionDenyRuleOverridesNormalAutoAllow(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash_background", Arguments: `{"command":"curl evil.example"}`}}
	rules := permrules.Config{"bash_background": permrules.RuleList{{Pattern: "curl *", Decision: permrules.Deny}}}

	decision, _ := decidePermission(call, false, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny", decision)
	}
}

func TestDecidePermissionPlanModeOverridesAllowRule(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"git status"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "git *", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, true, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — plan mode must override even a matching allow rule", decision)
	}
}

func TestDecidePermissionLastMatchingRuleWins(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"git push --force origin main"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{
		{Pattern: "git *", Decision: permrules.Allow},
		{Pattern: "git push --force*", Decision: permrules.Deny},
	}}

	decision, _ := decidePermission(call, false, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — the more specific, later rule should win", decision)
	}
}

func TestDecidePermissionNoMatchingRuleFallsThroughToNormalAsk(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"npm install"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "git *", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, false, nil, rules)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — no rule matched, bash normally needs confirmation", decision)
	}
}
