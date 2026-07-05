package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BikeshR/chisel/internal/agent"
	"github.com/BikeshR/chisel/internal/mcp"
)

// unsetEnvForTest unsets key for the duration of the test, restoring
// whatever it was (set or not) afterward — t.Setenv alone can't express
// "make sure this is unset", only "set it to this value".
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	original, wasSet := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(key, original)
		}
	})
}

func TestUnquote(t *testing.T) {
	cases := map[string]string{
		`"sk-abc123"`: "sk-abc123",
		`'sk-abc123'`: "sk-abc123",
		"sk-abc123":   "sk-abc123",
		`"`:           `"`, // a single stray quote isn't a matching pair
		"":            "",
		`""`:          "",
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadDotEnvStripsQuotes is the regression test for a real,
// easy-to-hit bug: CHISEL_API_KEY="sk-..." in ~/.chisel.env is a
// natural way to write it (shell-style, quoting a value with special
// characters), but without unquoting, the literal quote characters
// become part of the key and every request fails authentication with
// no indication why.
func TestLoadDotEnvStripsQuotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnvForTest(t, "CHISEL_API_KEY")
	unsetEnvForTest(t, "CHISEL_MODEL")

	body := "CHISEL_API_KEY=\"sk-test-key\"\nCHISEL_MODEL='some-model'\n"
	if err := os.WriteFile(filepath.Join(home, ".chisel.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loadDotEnv()

	if got := os.Getenv("CHISEL_API_KEY"); got != "sk-test-key" {
		t.Errorf("CHISEL_API_KEY = %q, want quotes stripped", got)
	}
	if got := os.Getenv("CHISEL_MODEL"); got != "some-model" {
		t.Errorf("CHISEL_MODEL = %q, want quotes stripped", got)
	}
}

func TestLoadDotEnvRealEnvironmentWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHISEL_API_KEY", "real-value")

	body := "CHISEL_API_KEY=\"from-file\"\n"
	if err := os.WriteFile(filepath.Join(home, ".chisel.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	loadDotEnv()

	if got := os.Getenv("CHISEL_API_KEY"); got != "real-value" {
		t.Errorf("CHISEL_API_KEY = %q, want the real environment variable to win over the file", got)
	}
}

func TestConfirmHooksTrustAcceptsYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !confirmHooksTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Error("expected trust to be granted on 'y'")
	}

	// A second call must not re-prompt — it's already trusted.
	if !confirmHooksTrustFrom(workDir, strings.NewReader("")) {
		t.Error("expected trust to persist without needing to answer again")
	}
}

func TestConfirmHooksTrustRejectsNoAndAnythingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if confirmHooksTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected trust to be denied on 'n'")
	}
	if confirmHooksTrustFrom(workDir, strings.NewReader("\n")) {
		t.Error("expected trust to be denied on a bare enter")
	}
}

func TestConfirmHooksTrustRePromptsOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"exit 0"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !confirmHooksTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Fatal("expected initial trust to be granted")
	}

	// Change the hooks content — must require a fresh approval.
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"*","command":"curl evil.example"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if confirmHooksTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected changed hooks content to require re-approval, not reuse the old trust")
	}
}

// TestConfirmHooksTrustShowsMatchAndCommand is the regression test for
// a UX gap left over after the MCP trust prompt was enriched: hooks are
// arbitrary shell commands that run automatically, so a trust decision
// with no visibility into which ones (and against which tool calls)
// isn't an informed one — the same reasoning the MCP prompt was fixed
// for, transplanted here.
func TestConfirmHooksTrustShowsMatchAndCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"preToolUse":[{"match":"bash","command":"curl evil.example"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	confirmHooksTrustFrom(workDir, strings.NewReader("n\n"))
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	printed := string(out)
	if !strings.Contains(printed, "bash") || !strings.Contains(printed, "curl evil.example") {
		t.Errorf("printed prompt = %q, want the hook's match and command shown", printed)
	}
}

// TestConfirmHooksTrustShowsNewEventTypes is the regression test for a
// gap the sessionStart/sessionEnd/userPromptSubmit addition could
// otherwise leave behind: a hooks.json containing ONLY these new event
// types (no preToolUse/postToolUse at all) must still list its actual
// commands in the trust prompt, not fall back to the generic message —
// the enrichment has to cover every event type HasAny recognizes, not
// just the original two.
func TestConfirmHooksTrustShowsNewEventTypes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"hooks":{"userPromptSubmit":[{"command":"check-secrets.sh"}],"preCompact":[{"command":"backup-transcript.sh"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	confirmHooksTrustFrom(workDir, strings.NewReader("n\n"))
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	printed := string(out)
	if !strings.Contains(printed, "check-secrets.sh") {
		t.Errorf("printed prompt = %q, want the userPromptSubmit hook's command shown, not the generic fallback message", printed)
	}
	if !strings.Contains(printed, "backup-transcript.sh") {
		t.Errorf("printed prompt = %q, want the preCompact hook's command shown too", printed)
	}
}

func TestRunHeadlessCoreReturnsFinalAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"the answer is 42\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	answer, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "what is the answer?", nil)
	if err != nil {
		t.Fatalf("runHeadlessCore: %v", err)
	}
	if answer != "the answer is 42" {
		t.Errorf("answer = %q, want %q", answer, "the answer is 42")
	}
}

// TestRunHeadlessCoreUsesReadOnlyTools confirms headless mode's request
// declares only the read-only tool set, not the full bash/edit set —
// there's no terminal to show a permission prompt to in a
// non-interactive invocation, so nothing offered can need one.
func TestRunHeadlessCoreUsesReadOnlyTools(t *testing.T) {
	var toolNames []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		for _, tool := range req.Tools {
			toolNames = append(toolNames, tool.Function.Name)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	if _, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi", nil); err != nil {
		t.Fatalf("runHeadlessCore: %v", err)
	}

	for _, unwanted := range []string{"bash", "str_replace_based_edit_tool", "dispatch_subagent"} {
		for _, name := range toolNames {
			if name == unwanted {
				t.Errorf("request declared tool %q, want it excluded from headless mode", unwanted)
			}
		}
	}
	found := false
	for _, name := range toolNames {
		if name == "glob" {
			found = true
		}
	}
	if !found {
		t.Errorf("toolNames = %+v, want glob (a read-only tool) included", toolNames)
	}
}

// TestRunHeadlessCoreRejectsHallucinatedMutatingToolCall is the
// regression test for the same gap TestRunHeadlessCoreUsesReadOnlyTools
// checks at the request-schema level: even though bash/edit tools are
// never *offered*, agent.Execute dispatches purely by name and would
// still run a real edit if the model emitted one anyway. Headless mode
// shares agent.RunLoop's fix (a whitelist check against client.tools)
// with RunSubagent, so this proves it end to end through runHeadlessCore.
func TestRunHeadlessCoreRejectsHallucinatedMutatingToolCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		if call == 0 {
			call++
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"str_replace_based_edit_tool","arguments":"{\"command\":\"str_replace\",\"path\":\"notes.txt\",\"old_str\":\"original\",\"new_str\":\"hacked\"}"}}]}}]}` + "\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"done"}}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	if _, _, err := runHeadlessCore(dir, "minimax-m3", "do something", nil); err != nil {
		t.Fatalf("runHeadlessCore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("notes.txt = %q, want it untouched — a tool never offered to headless mode must not execute", data)
	}
}

// TestRunHeadlessCoreReportsToolCallEventsToOnEvent is the direct test
// of -json-stream's underlying plumbing: onEvent must see a "start"
// then an "end" for each real tool call the loop actually executes.
func TestRunHeadlessCoreReportsToolCallEventsToOnEvent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		if call == 0 {
			call++
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"glob","arguments":"{\"pattern\":\"*.txt\"}"}}]}}]}` + "\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"done"}}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	var events []agent.LoopEvent
	_, _, err := runHeadlessCore(dir, "minimax-m3", "list txt files", func(ev agent.LoopEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("runHeadlessCore: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("events = %+v, want exactly 2 (start, end)", events)
	}
	if events[0].Phase != "start" || events[0].Tool != "glob" {
		t.Errorf("events[0] = %+v, want a start event for glob", events[0])
	}
	if events[1].Phase != "end" || events[1].Tool != "glob" || events[1].IsError {
		t.Errorf("events[1] = %+v, want a successful end event for glob", events[1])
	}
}

func TestFormatNDJSONToolCallStartAndEnd(t *testing.T) {
	startLine, err := formatNDJSONToolCall(agent.LoopEvent{Phase: "start", Tool: "glob"})
	if err != nil {
		t.Fatalf("formatNDJSONToolCall: %v", err)
	}
	var start struct {
		Type  string `json:"type"`
		Phase string `json:"phase"`
		Tool  string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(startLine), &start); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, startLine)
	}
	if start.Type != "tool_call" || start.Phase != "start" || start.Tool != "glob" {
		t.Errorf("start = %+v", start)
	}

	endLine, err := formatNDJSONToolCall(agent.LoopEvent{Phase: "end", Tool: "glob", Result: "a.txt\nb.txt", IsError: false})
	if err != nil {
		t.Fatalf("formatNDJSONToolCall: %v", err)
	}
	var end struct {
		Type    string `json:"type"`
		Phase   string `json:"phase"`
		Tool    string `json:"tool"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(endLine), &end); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, endLine)
	}
	if end.Type != "tool_call" || end.Phase != "end" || end.Result != "a.txt\nb.txt" || end.IsError {
		t.Errorf("end = %+v", end)
	}
}

func TestFormatNDJSONResultHasTypeDiscriminator(t *testing.T) {
	line, err := formatNDJSONResult("the answer is 42", agent.Usage{InputTokens: 120, OutputTokens: 8}, nil)
	if err != nil {
		t.Fatalf("formatNDJSONResult: %v", err)
	}
	var got struct {
		Type   string `json:"type"`
		Answer string `json:"answer"`
		Usage  struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, line)
	}
	if got.Type != "result" {
		t.Errorf("Type = %q, want \"result\" so a line-oriented parser can tell this apart from a tool_call line", got.Type)
	}
	if got.Answer != "the answer is 42" || got.Usage.InputTokens != 120 || got.Usage.OutputTokens != 8 {
		t.Errorf("got = %+v", got)
	}
}

func TestFormatNDJSONResultFailure(t *testing.T) {
	line, err := formatNDJSONResult("", agent.Usage{}, fmt.Errorf("upstream returned 500"))
	if err != nil {
		t.Fatalf("formatNDJSONResult: %v", err)
	}
	var got struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, line)
	}
	if got.Type != "result" || got.Error != "upstream returned 500" {
		t.Errorf("got = %+v", got)
	}
}

func TestRunHeadlessCorePropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	if _, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi", nil); err == nil {
		t.Error("expected an error from a failing request")
	}
}

func TestConfirmPermRulesTrustAcceptsYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	rulesPath := filepath.Join(workDir, ".chisel", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesPath, []byte(`{"bash":{"git *":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !confirmPermRulesTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Error("expected trust to be granted on 'y'")
	}

	// A second call must not re-prompt — it's already trusted.
	if !confirmPermRulesTrustFrom(workDir, strings.NewReader("")) {
		t.Error("expected trust to persist without needing to answer again")
	}
}

func TestConfirmPermRulesTrustRejectsNoAndAnythingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	rulesPath := filepath.Join(workDir, ".chisel", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesPath, []byte(`{"bash":{"git *":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if confirmPermRulesTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected trust to be denied on 'n'")
	}
	if confirmPermRulesTrustFrom(workDir, strings.NewReader("\n")) {
		t.Error("expected trust to be denied on a bare enter")
	}
}

func TestConfirmPermRulesTrustRePromptsOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	rulesPath := filepath.Join(workDir, ".chisel", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesPath, []byte(`{"bash":{"git *":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !confirmPermRulesTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Fatal("expected initial trust to be granted")
	}

	// Change the rules content — must require a fresh approval.
	if err := os.WriteFile(rulesPath, []byte(`{"bash":{"*":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if confirmPermRulesTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("expected changed rules content to require re-approval, not reuse the old trust")
	}
}

// TestConfirmPermRulesTrustShowsToolPatternAndDecision mirrors the
// hooks-prompt enrichment: a rule that silently allows a call (bypassing
// confirmation the same way a hook can silently execute code) needs the
// same visibility into what it actually approves.
func TestConfirmPermRulesTrustShowsToolPatternAndDecision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	rulesPath := filepath.Join(workDir, ".chisel", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesPath, []byte(`{"bash":{"git push --force*":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	confirmPermRulesTrustFrom(workDir, strings.NewReader("n\n"))
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	printed := string(out)
	if !strings.Contains(printed, "bash") || !strings.Contains(printed, "git push --force*") || !strings.Contains(printed, "allow") {
		t.Errorf("printed prompt = %q, want the rule's tool, pattern, and decision shown", printed)
	}
}

// TestConfirmHooksAndPermRulesTrustAreIndependent is why the two use
// separate trust files (trusted_hooks.json / trusted_permrules.json):
// approving one must never implicitly approve the other, even with
// byte-identical content.
func TestConfirmHooksAndPermRulesTrustAreIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	content := []byte(`{"same":"content"}`)

	hooksPath := filepath.Join(workDir, ".chisel", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if !confirmHooksTrustFrom(workDir, strings.NewReader("y\n")) {
		t.Fatal("expected hooks trust to be granted")
	}

	rulesPath := filepath.Join(workDir, ".chisel", "permissions.json")
	if err := os.WriteFile(rulesPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if confirmPermRulesTrustFrom(workDir, strings.NewReader("n\n")) {
		t.Error("trusting hooks.json must not implicitly trust permissions.json, even with identical content")
	}
}

func TestConfirmMCPTrustAcceptsYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	mcpPath := filepath.Join(workDir, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"local":{"command":"my-mcp-server"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if !confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("y\n")) {
		t.Error("expected trust to be granted on 'y'")
	}

	// A second call must not re-prompt — it's already trusted.
	if !confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("")) {
		t.Error("expected trust to persist without needing to answer again")
	}
}

func TestConfirmMCPTrustRejectsNoAndAnythingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	mcpPath := filepath.Join(workDir, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"local":{"command":"my-mcp-server"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("n\n")) {
		t.Error("expected trust to be denied on 'n'")
	}
	if confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("\n")) {
		t.Error("expected trust to be denied on a bare enter")
	}
}

func TestConfirmMCPTrustRePromptsOnContentChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	mcpPath := filepath.Join(workDir, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"local":{"command":"my-mcp-server"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("y\n")) {
		t.Fatal("expected initial trust to be granted")
	}

	// Change the config content — must require a fresh approval.
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"local":{"command":"a-different-command"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("n\n")) {
		t.Error("expected changed mcp.json content to require re-approval, not reuse the old trust")
	}
}

// TestConfirmMCPTrustShowsServerNamesAndCommands is the regression test
// for a real UX gap: the trust prompt used to print only a generic
// sentence, giving the user no visibility into which servers/commands
// they were actually approving — the same reasoning chisel's own
// permission prompt was fixed for.
func TestConfirmMCPTrustShowsServerNamesAndCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	mcpPath := filepath.Join(workDir, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	confirmMCPTrustFrom(workDir, mcp.Config{}, strings.NewReader("n\n"))
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	printed := string(out)
	if !strings.Contains(printed, "github") {
		t.Errorf("printed prompt = %q, want the server name mentioned", printed)
	}
	if !strings.Contains(printed, "npx") || !strings.Contains(printed, "@modelcontextprotocol/server-github") {
		t.Errorf("printed prompt = %q, want the command and args shown", printed)
	}
}

// TestConfirmMCPTrustWarnsWhenProjectServerOverridesUserServer is the
// regression test for the security half of the same fix: a project
// config can plant a server under a name that shadows one already
// trusted from the user's own ~/.chisel/mcp.json, running a completely
// different command under a familiar name — the prompt must call that
// out explicitly rather than looking identical to a brand-new server.
func TestConfirmMCPTrustWarnsWhenProjectServerOverridesUserServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := t.TempDir()
	mcpPath := filepath.Join(workDir, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"github":{"command":"a-suspicious-binary"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	userCfg := mcp.Config{MCPServers: map[string]mcp.ServerConfig{
		"github": {Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
	}}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	confirmMCPTrustFrom(workDir, userCfg, strings.NewReader("n\n"))
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	printed := string(out)
	if !strings.Contains(printed, "overrides") {
		t.Errorf("printed prompt = %q, want a warning that this server overrides a user-scoped one of the same name", printed)
	}
}

func TestReadPipedInputReadsAndFramesContent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString("diff --git a/x.go b/x.go\n+added a line")
		_ = w.Close()
	}()

	got, err := readPipedInput(r)
	if err != nil {
		t.Fatalf("readPipedInput: %v", err)
	}
	if !strings.Contains(got, "diff --git a/x.go b/x.go") {
		t.Errorf("got = %q, want it to contain the piped content", got)
	}
	if !strings.Contains(got, "piped stdin") {
		t.Errorf("got = %q, want a framing marker distinguishing it from the prompt", got)
	}
}

func TestReadPipedInputEmptyPipeReturnsEmpty(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := readPipedInput(r)
	if err != nil {
		t.Fatalf("readPipedInput: %v", err)
	}
	if got != "" {
		t.Errorf("got = %q, want empty for an empty pipe", got)
	}
}

func TestFormatHeadlessJSONSuccess(t *testing.T) {
	line, err := formatHeadlessJSON("the answer is 42", agent.Usage{InputTokens: 120, OutputTokens: 8}, nil)
	if err != nil {
		t.Fatalf("formatHeadlessJSON: %v", err)
	}

	var got struct {
		Answer string `json:"answer"`
		Usage  struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, line)
	}
	if got.Answer != "the answer is 42" {
		t.Errorf("Answer = %q", got.Answer)
	}
	if got.Usage.InputTokens != 120 || got.Usage.OutputTokens != 8 {
		t.Errorf("Usage = %+v", got.Usage)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty on success", got.Error)
	}
	if strings.Contains(line, `"error"`) {
		t.Errorf("line = %q, want the omitempty error field entirely absent on success, not just empty", line)
	}
}

func TestFormatHeadlessJSONFailure(t *testing.T) {
	line, err := formatHeadlessJSON("", agent.Usage{}, fmt.Errorf("upstream returned 500"))
	if err != nil {
		t.Fatalf("formatHeadlessJSON: %v", err)
	}

	var got struct {
		Answer string `json:"answer"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("output isn't valid JSON: %v — got %q", err, line)
	}
	if got.Error != "upstream returned 500" {
		t.Errorf("Error = %q", got.Error)
	}
	if got.Answer != "" {
		t.Errorf("Answer = %q, want empty on failure", got.Answer)
	}
}

func TestRunHeadlessCoreReturnsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":55,\"completion_tokens\":11}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	_, usage, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi", nil)
	if err != nil {
		t.Fatalf("runHeadlessCore: %v", err)
	}
	if usage.InputTokens != 55 || usage.OutputTokens != 11 {
		t.Errorf("usage = %+v, want {55 11}", usage)
	}
}

func TestMaybeAddGoplsNoGoMod(t *testing.T) {
	r := &mcp.Registry{}
	maybeAddGopls(r, t.TempDir())
	if len(r.Tools()) != 0 {
		t.Errorf("Tools() = %+v, want none added without a go.mod", r.Tools())
	}
}

func TestMaybeAddGoplsNotOnPath(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // a directory guaranteed not to contain gopls

	r := &mcp.Registry{}
	maybeAddGopls(r, workDir)
	if len(r.Tools()) != 0 {
		t.Errorf("Tools() = %+v, want none added when gopls isn't on PATH", r.Tools())
	}
}

// TestMaybeAddGoplsRealServer is a real, live test against an actually
// installed gopls — skipped if it isn't available, since chisel doesn't
// bundle or require it (same reasoning as any other MCP server: it's
// something the user separately has, not something chisel installs).
func TestMaybeAddGoplsRealServer(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed — skipping live verification")
	}

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &mcp.Registry{}
	maybeAddGopls(r, workDir)
	defer r.Close()

	found := false
	for _, tool := range r.Tools() {
		if tool.Name == "mcp__gopls__go_diagnostics" {
			found = true
		}
	}
	if !found {
		t.Errorf("Tools() = %+v, want mcp__gopls__go_diagnostics from the real gopls mcp server", r.Tools())
	}
}

// TestMaybeAddGoplsDoesNotOverrideUserConfiguredServer verifies the
// collision guard from maybeAddGopls's own call site: pre-seed the
// registry with a "gopls" entry (as if the user had already configured
// one in ~/.chisel/mcp.json themselves) using gopls itself, so the
// pre-seeded entry is a real, working MCP server — then call
// maybeAddGopls again and confirm it doesn't panic or attempt a second,
// redundant start now that the name is taken.
func TestMaybeAddGoplsDoesNotOverrideUserConfiguredServer(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed — skipping")
	}

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &mcp.Registry{}
	if err := r.AddServer("gopls", mcp.ServerConfig{Command: "gopls", Args: []string{"mcp"}}); err != nil {
		t.Fatalf("pre-seeding a user-configured 'gopls' server: %v", err)
	}
	defer r.Close()

	before := len(r.Tools())
	maybeAddGopls(r, workDir) // must not panic or add a redundant second server
	after := len(r.Tools())

	if before != after {
		t.Errorf("tool count changed from %d to %d, want unchanged — a second gopls server must not be started", before, after)
	}
}
