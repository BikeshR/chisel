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

func TestRunHeadlessCoreReturnsFinalAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"the answer is 42\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	answer, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "what is the answer?")
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

	if _, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi"); err != nil {
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

func TestRunHeadlessCorePropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("CHISEL_BASE_URL", server.URL)
	t.Setenv("CHISEL_API_KEY", "test-key")

	if _, _, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi"); err == nil {
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

	_, usage, err := runHeadlessCore(t.TempDir(), "minimax-m3", "hi")
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
