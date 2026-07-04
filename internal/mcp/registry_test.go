package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryToolsAreNamespaced(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	r := &Registry{servers: map[string]*Server{"fake": s}}
	tools := r.Tools()
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1: %+v", len(tools), tools)
	}
	if want := "mcp__fake__echo"; tools[0].Name != want {
		t.Errorf("tool name = %q, want %q", tools[0].Name, want)
	}
}

func TestRegistryToolsAreSortedDeterministically(t *testing.T) {
	sA, err := Start("zzz-server", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sA.Close()
	sB, err := Start("aaa-server", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sB.Close()

	r := &Registry{servers: map[string]*Server{"zzz-server": sA, "aaa-server": sB}}

	// r.servers is a map — Go randomizes its iteration order on purpose —
	// so call Tools() repeatedly and confirm it's the same order every
	// time, not just correct once by chance.
	var want []string
	for i := 0; i < 10; i++ {
		tools := r.Tools()
		got := make([]string, len(tools))
		for j, tl := range tools {
			got[j] = tl.Name
		}
		if i == 0 {
			want = got
			continue
		}
		if len(got) != len(want) {
			t.Fatalf("call %d: got %d tools, want %d", i, len(got), len(want))
		}
		for j := range got {
			if got[j] != want[j] {
				t.Fatalf("call %d: order = %v, want the same order every call: %v", i, got, want)
			}
		}
	}

	if len(want) != 2 || want[0] != "mcp__aaa-server__echo" || want[1] != "mcp__zzz-server__echo" {
		t.Errorf("order = %v, want alphabetical by name", want)
	}
}

func TestRegistryCall(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	r := &Registry{servers: map[string]*Server{"fake": s}}
	content, isError, err := r.Call(context.Background(), "mcp__fake__echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if isError {
		t.Error("isError = true, want false")
	}
	if !strings.Contains(content, `"x":1`) {
		t.Errorf("content = %q", content)
	}
}

func TestRegistryCallUnknownServer(t *testing.T) {
	r := &Registry{servers: map[string]*Server{}}
	_, _, err := r.Call(context.Background(), "mcp__nonexistent__tool", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected an error calling a tool on a server that isn't running")
	}
}

func TestRegistryCallMalformedName(t *testing.T) {
	r := &Registry{servers: map[string]*Server{}}
	_, _, err := r.Call(context.Background(), "not-an-mcp-tool-name", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected an error for a non-mcp-prefixed tool name")
	}
}

func TestIsToolName(t *testing.T) {
	cases := map[string]bool{
		"mcp__github__list_issues":    true,
		"bash":                        false,
		"str_replace_based_edit_tool": false,
		"":                            false,
	}
	for name, want := range cases {
		if got := IsToolName(name); got != want {
			t.Errorf("IsToolName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestLoadAndStartAllRejectsServerNameContainingDoubleUnderscore is the
// regression test for a real routing bug: SplitToolName cuts a prefixed
// tool name at the *first* "__", so a server literally named
// "my__server" would misroute every one of its own tools (the cut lands
// between "my" and "server__tool", not between the server and the
// tool). Rejecting the name at load time is cheaper and clearer than
// ever letting such a server register.
func TestLoadAndStartAllRejectsServerNameContainingDoubleUnderscore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"mcpServers":{"my__server":{"command":"echo","args":["hi"]}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r, errs := LoadAndStartAll()
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want exactly 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "__") {
		t.Errorf("error = %v, want it to mention the \"__\" restriction", errs[0])
	}
	if len(r.servers) != 0 {
		t.Errorf("registry has %d servers, want 0 — the malformed name shouldn't have started", len(r.servers))
	}
}

func TestRegistryClose(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r := &Registry{servers: map[string]*Server{"fake": s}}
	r.Close() // should not panic or hang
}

// TestLoadAndStartAllStartsServersConcurrently is the real proof for
// the concurrency fix: three servers, each artificially slow to
// initialize (simulating an npx cold download), started via one
// LoadAndStartAll call. Sequential startup would take roughly 3x the
// per-server delay; concurrent startup should take roughly 1x.
func TestLoadAndStartAllStartsServersConcurrently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const delayMS = 300
	cfgPath := filepath.Join(home, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}

	server := func() map[string]any {
		return map[string]any{
			"command": os.Args[0],
			"env": map[string]string{
				"CHISEL_MCP_FAKE_SERVER":          "1",
				"CHISEL_MCP_FAKE_SERVER_DELAY_MS": fmt.Sprintf("%d", delayMS),
			},
		}
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"one":   server(),
			"two":   server(),
			"three": server(),
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	r, errs := LoadAndStartAll()
	elapsed := time.Since(start)
	defer r.Close()

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(r.servers) != 3 {
		t.Fatalf("got %d servers, want 3", len(r.servers))
	}

	// A generous margin above one delay, but well under what sequential
	// startup (3x) would take — this is a real wall-clock assertion,
	// not a mock, so it needs enough slack to not be flaky while still
	// clearly distinguishing concurrent from sequential.
	if elapsed > 2*delayMS*time.Millisecond {
		t.Errorf("LoadAndStartAll took %s for 3 servers each delayed %dms — want close to one delay (concurrent), not the sum (sequential)", elapsed, delayMS)
	}
}

func TestRegistryAddServer(t *testing.T) {
	r := &Registry{servers: map[string]*Server{}}
	if err := r.AddServer("fake", fakeServerConfig()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	defer r.Close()

	tools := r.Tools()
	if len(tools) != 1 || tools[0].Name != "mcp__fake__echo" {
		t.Errorf("Tools() = %+v, want mcp__fake__echo", tools)
	}
}

func TestRegistryAddServerRefusesCollisionWithExistingServer(t *testing.T) {
	r := &Registry{servers: map[string]*Server{}}
	if err := r.AddServer("fake", fakeServerConfig()); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	defer r.Close()

	err := r.AddServer("fake", fakeServerConfig())
	if err == nil {
		t.Fatal("expected an error adding a server under an already-used name")
	}

	// The original server must still be the one running — a second
	// AddServer with the same name must not have replaced it.
	if len(r.servers) != 1 {
		t.Errorf("servers = %+v, want the original left untouched", r.servers)
	}
}

// TestRegistryAddServerOnZeroValueRegistry is the regression test for a
// real nil-map panic: a bare &Registry{} (as opposed to one built via
// LoadAndStartAll, which always initializes servers) is a natural way
// to construct one just to add a single server, and AddServer must not
// require the caller to know LoadAndStartAll's internal initialization
// detail to avoid a panic on the first add.
func TestRegistryAddServerOnZeroValueRegistry(t *testing.T) {
	r := &Registry{}
	if err := r.AddServer("fake", fakeServerConfig()); err != nil {
		t.Fatalf("AddServer on a zero-value Registry: %v", err)
	}
	defer r.Close()

	if len(r.Tools()) != 1 {
		t.Errorf("Tools() = %+v, want one tool from the newly added server", r.Tools())
	}
}
