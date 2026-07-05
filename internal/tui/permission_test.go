package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/permrules"
)

func TestDecidePermissionAllowsReadOnlyTools(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}}
	decision, _ := decidePermission(call, agent.ModeNormal, nil, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for a read-only tool", decision)
	}
}

func TestDecidePermissionAsksForBash(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	decision, _ := decidePermission(call, agent.ModeNormal, nil, nil)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk for bash", decision)
	}
}

func TestDecidePermissionDeniesInPlanMode(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}}
	decision, reason := decidePermission(call, agent.ModePlan, nil, nil)
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
	decision, _ := decidePermission(call, agent.ModePlan, allowlist, nil)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — plan mode must override even an allowlisted command", decision)
	}
}

// TestDecidePermissionAcceptEditsAllowsFileEdit is the direct test of
// the new mode: a mutating editor call is auto-approved without
// touching the allowlist or persistent rules at all.
func TestDecidePermissionAcceptEditsAllowsFileEdit(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"a.go","old_str":"x","new_str":"y"}`,
	}}
	decision, _ := decidePermission(call, agent.ModeAcceptEdits, nil, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for a file edit in accept-edits mode", decision)
	}
}

// TestDecidePermissionAcceptEditsStillAsksForBash is the safety-critical
// half: accept-edits must never widen to bash, since chisel has no bash
// sandbox — the exact reason docs/design.md defers sandboxing until an
// auto-approve mode exists, which this deliberately isn't one of for
// bash specifically.
func TestDecidePermissionAcceptEditsStillAsksForBash(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /tmp/x"}`}}
	decision, _ := decidePermission(call, agent.ModeAcceptEdits, nil, nil)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — accept-edits must never auto-approve bash", decision)
	}
}

// TestDecidePermissionAcceptEditsStillAsksForMCP confirms the same for
// MCP tools — chisel can't reason about what an arbitrary server's tool
// does, so accept-edits (unlike a general auto-approve mode) doesn't
// extend to it either.
func TestDecidePermissionAcceptEditsStillAsksForMCP(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "mcp__github__create_issue", Arguments: `{}`}}
	decision, _ := decidePermission(call, agent.ModeAcceptEdits, nil, nil)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — accept-edits must not auto-approve MCP calls", decision)
	}
}

// TestDecidePermissionAcceptEditsAllowsViewWithoutSpecialCasing confirms
// the editor tool's read-only "view" subcommand is unaffected — it was
// already auto-allowed before accept-edits existed.
func TestDecidePermissionAcceptEditsAllowsViewWithoutSpecialCasing(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"view","path":"a.go"}`,
	}}
	decision, _ := decidePermission(call, agent.ModeAcceptEdits, nil, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for view", decision)
	}
}

func TestDecidePermissionAllowsAllowlistedBashCommand(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./..."}`}}
	allowlist := map[string]bool{"bash:go test ./...": true}
	decision, _ := decidePermission(call, agent.ModeNormal, allowlist, nil)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow for an allowlisted command", decision)
	}
}

func TestDecidePermissionAllowlistIsExactCommandMatch(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"go test ./... -v"}`}}
	allowlist := map[string]bool{"bash:go test ./...": true}
	decision, _ := decidePermission(call, agent.ModeNormal, allowlist, nil)
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

// TestPersistableRuleForExcludesMCPAndReadOnlyTools confirms only
// bash/bash_background/str_replace_based_edit_tool support a
// persistent rule — an MCP tool's own arguments (arbitrary per-server
// shape) and a read-only tool that already needs no permission at all
// have no natural single string to write a glob against.
func TestPersistableRuleForExcludesMCPAndReadOnlyTools(t *testing.T) {
	cases := []agent.ToolCall{
		{Function: agent.ToolCallFunction{Name: "mcp__github__create_issue", Arguments: `{}`}},
		{Function: agent.ToolCallFunction{Name: "glob", Arguments: `{"pattern":"**/*.go"}`}},
	}
	for _, call := range cases {
		if _, _, ok := persistableRuleFor(call); ok {
			t.Errorf("expected %s to not support a persistent rule", call.Function.Name)
		}
	}
}

// TestPersistableRuleForIncludesEditPath is the direct regression test
// for the feature: a file edit's path is just as natural a single
// string to glob-match as a bash command's own text, so
// str_replace_based_edit_tool must support a persistent rule keyed by
// path — unlike allowlistKey's session-only "always allow," which
// deliberately still excludes edits (each one has a materially
// different diff, so an exact-match allowlist entry doesn't carry the
// same meaning there); a path glob is a different mechanism entirely
// and doesn't share that problem.
func TestPersistableRuleForIncludesEditPath(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "str_replace_based_edit_tool", Arguments: `{"command":"str_replace","path":"src/main.go","old_str":"x","new_str":"y"}`}}
	toolName, pattern, ok := persistableRuleFor(call)
	if !ok {
		t.Fatal("expected str_replace_based_edit_tool to support a persistent rule")
	}
	if toolName != "str_replace_based_edit_tool" || pattern != "src/main.go" {
		t.Errorf("toolName=%q pattern=%q, want str_replace_based_edit_tool / src/main.go", toolName, pattern)
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

// TestPressingPWritesPermanentEditRuleAndRuns is
// TestPressingPWritesPermanentRuleAndRuns's counterpart for a file
// edit — confirms the "p" prompt option actually persists a path-glob
// rule end-to-end (in-memory, on disk, and re-loadable) rather than
// just decidePermission accepting one in isolation.
func TestPressingPWritesPermanentEditRuleAndRuns(t *testing.T) {
	workDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: workDir,
		state:   stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{
				Name:      "str_replace_based_edit_tool",
				Arguments: `{"command":"str_replace","path":"main.go","old_str":"package main","new_str":"package other"}`,
			}},
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

	if decision, matched := permrules.Match(gotModel.permRules, "str_replace_based_edit_tool", "main.go"); !matched || decision != permrules.Allow {
		t.Errorf("in-memory permRules Match = (%v, %v), want (Allow, true)", decision, matched)
	}

	loaded, found, err := permrules.Load(workDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !found {
		t.Fatal("expected permissions.json to have been written")
	}
	if decision, matched := permrules.Match(loaded, "str_replace_based_edit_tool", "main.go"); !matched || decision != permrules.Allow {
		t.Errorf("Match after reload = (%v, %v), want (Allow, true)", decision, matched)
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

// TestPressingPOnIneligibleCallStillApproves used to use a file-edit
// call as its "ineligible" fixture — str_replace_based_edit_tool is no
// longer ineligible now that persistableRuleFor supports a path-scoped
// rule for it (see TestPersistableRuleForIncludesEditPath), so this now
// uses an MCP call, which genuinely has no natural single string to
// write a persistent glob rule against.
func TestPressingPOnIneligibleCallStillApproves(t *testing.T) {
	m := Model{
		client:  agent.New("minimax-m3"),
		workDir: t.TempDir(),
		state:   stateAwaitingPermission,
		pendingUses: []agent.ToolCall{
			{ID: "call_1", Function: agent.ToolCallFunction{Name: "mcp__github__create_issue", Arguments: `{"title":"x"}`}},
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
	if _, found, _ := permrules.Load(m.workDir); found {
		t.Error("expected no permissions.json written for an ineligible call")
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

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow — a matching allow rule", decision)
	}
}

func TestDecidePermissionRuleDeniesBashCommand(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"rm -rf /tmp/x"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "rm -rf *", Decision: permrules.Deny}}}

	decision, reason := decidePermission(call, agent.ModeNormal, nil, rules)
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

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny", decision)
	}
}

// TestDecidePermissionRuleAllowsEditPath is the direct regression test
// for path-scoped edit rules: a persistent rule like "allow edits under
// src/**" should auto-approve a matching file edit exactly the way a
// bash command rule already does.
func TestDecidePermissionRuleAllowsEditPath(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"src/main.go","old_str":"x","new_str":"y"}`,
	}}
	rules := permrules.Config{"str_replace_based_edit_tool": permrules.RuleList{{Pattern: "src/*", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionAllow {
		t.Errorf("decision = %v, want permissionAllow — a matching allow rule for the edit's path", decision)
	}
}

// TestDecidePermissionRuleDeniesEditPath confirms the same for a deny
// rule — "never touch vendor/" should refuse the edit outright, the
// same way a denied bash command does.
func TestDecidePermissionRuleDeniesEditPath(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"vendor/lib.go","old_str":"x","new_str":"y"}`,
	}}
	rules := permrules.Config{"str_replace_based_edit_tool": permrules.RuleList{{Pattern: "vendor/*", Decision: permrules.Deny}}}

	decision, reason := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — a matching deny rule for the edit's path", decision)
	}
	if !strings.Contains(reason, "permissions.json") {
		t.Errorf("reason = %q, want it to mention permissions.json", reason)
	}
}

// TestDecidePermissionEditRuleDoesNotMatchUnrelatedPath confirms a rule
// scoped to one path glob doesn't accidentally allow an edit elsewhere
// — the normal ask path still applies for anything the rule doesn't
// actually match.
func TestDecidePermissionEditRuleDoesNotMatchUnrelatedPath(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"internal/secrets.go","old_str":"x","new_str":"y"}`,
	}}
	rules := permrules.Config{"str_replace_based_edit_tool": permrules.RuleList{{Pattern: "src/*", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — the rule's pattern doesn't match this path", decision)
	}
}

// TestDecidePermissionPlanModeOverridesEditAllowRule mirrors
// TestDecidePermissionPlanModeOverridesAllowRule for the new edit-path
// rule type — plan mode's absolute guarantee must hold regardless of
// which tool a rule was written for.
func TestDecidePermissionPlanModeOverridesEditAllowRule(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{
		Name:      "str_replace_based_edit_tool",
		Arguments: `{"command":"str_replace","path":"src/main.go","old_str":"x","new_str":"y"}`,
	}}
	rules := permrules.Config{"str_replace_based_edit_tool": permrules.RuleList{{Pattern: "src/*", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, agent.ModePlan, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — plan mode must override even an allowlisted edit path", decision)
	}
}

func TestDecidePermissionPlanModeOverridesAllowRule(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"git status"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "git *", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, agent.ModePlan, nil, rules)
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

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionDeny {
		t.Errorf("decision = %v, want permissionDeny — the more specific, later rule should win", decision)
	}
}

func TestDecidePermissionNoMatchingRuleFallsThroughToNormalAsk(t *testing.T) {
	call := agent.ToolCall{Function: agent.ToolCallFunction{Name: "bash", Arguments: `{"command":"npm install"}`}}
	rules := permrules.Config{"bash": permrules.RuleList{{Pattern: "git *", Decision: permrules.Allow}}}

	decision, _ := decidePermission(call, agent.ModeNormal, nil, rules)
	if decision != permissionAsk {
		t.Errorf("decision = %v, want permissionAsk — no rule matched, bash normally needs confirmation", decision)
	}
}
