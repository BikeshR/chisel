package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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

func TestRegistryClose(t *testing.T) {
	s, err := Start("fake", fakeServerConfig())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r := &Registry{servers: map[string]*Server{"fake": s}}
	r.Close() // should not panic or hang
}
