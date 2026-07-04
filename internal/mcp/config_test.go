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

func TestLoadProjectConfigMissingIsNotAnError(t *testing.T) {
	_, ok, err := LoadProjectConfig(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if ok {
		t.Error("ok = true with no project config file present")
	}
}

func TestLoadProjectConfigValid(t *testing.T) {
	workDir := t.TempDir()
	path := ProjectConfigPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"mcpServers":{"local":{"command":"my-server"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, ok, err := LoadProjectConfig(workDir)
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false for a valid project config file")
	}
	if cfg.MCPServers["local"].Command != "my-server" {
		t.Errorf("parsed config = %+v", cfg)
	}
}

func TestMergeProjectOverridesUserByName(t *testing.T) {
	user := Config{MCPServers: map[string]ServerConfig{
		"shared":    {Command: "user-version"},
		"user-only": {Command: "only-in-user"},
	}}
	project := Config{MCPServers: map[string]ServerConfig{
		"shared":       {Command: "project-version"},
		"project-only": {Command: "only-in-project"},
	}}

	merged := Merge(user, project)
	if len(merged.MCPServers) != 3 {
		t.Fatalf("got %d servers, want 3", len(merged.MCPServers))
	}
	if merged.MCPServers["shared"].Command != "project-version" {
		t.Errorf("shared server = %+v, want the project version to win", merged.MCPServers["shared"])
	}
	if merged.MCPServers["user-only"].Command != "only-in-user" {
		t.Error("expected a user-only server to survive the merge")
	}
	if merged.MCPServers["project-only"].Command != "only-in-project" {
		t.Error("expected a project-only server to survive the merge")
	}
}

func TestMCPTrustRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hash := ContentHash([]byte(`{"mcpServers":{"local":{"command":"x"}}}`))

	trusted, err := IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("should not be trusted before Trust is called")
	}

	if err := Trust(hash); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	trusted, err = IsTrusted(hash)
	if err != nil {
		t.Fatalf("IsTrusted after Trust: %v", err)
	}
	if !trusted {
		t.Error("expected the hash to be trusted after Trust was called")
	}
}
