package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, ok, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if ok {
		t.Error("ok = true with no config file present")
	}
}

func TestLoadConfigValid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"],"env":{"GITHUB_TOKEN":"x"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, ok, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false for a valid config file")
	}
	gh, present := cfg.MCPServers["github"]
	if !present {
		t.Fatal("expected a \"github\" server entry")
	}
	if gh.Command != "npx" || len(gh.Args) != 2 || gh.Env["GITHUB_TOKEN"] != "x" {
		t.Errorf("parsed server config = %+v", gh)
	}
}

func TestLoadConfigMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".chisel", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, ok, err := LoadConfig()
	if err == nil {
		t.Error("expected an error for a malformed config file, not a silent false")
	}
	if ok {
		t.Error("ok = true for a malformed config file")
	}
}
