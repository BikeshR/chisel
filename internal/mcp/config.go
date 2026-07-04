package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/BikeshR/chisel/internal/trust"
)

// ServerConfig is one entry under "mcpServers" — the same shape Claude
// Desktop and Claude Code use, so a config written for those works here
// unchanged.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// Config is the top-level shape of ~/.chisel/mcp.json.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ConfigPath returns where chisel looks for the MCP server config.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chisel", "mcp.json"), nil
}

// LoadConfig reads the MCP server config. ok is false if the file simply
// doesn't exist (no servers configured — not an error); a malformed file
// that does exist is still reported as an error, since that's likely a
// typo the user would want to know about rather than a silent no-op.
func LoadConfig() (cfg Config, ok bool, err error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, false, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// ProjectConfigPath returns <workDir>/.chisel/mcp.json — a project can
// ship its own MCP servers the same way it already can ship hooks
// (.chisel/hooks.json) or permission rules (.chisel/permissions.json),
// unlike before this existed, when MCP was the one config type with no
// project-scoped form at all.
func ProjectConfigPath(workDir string) string {
	return filepath.Join(workDir, ".chisel", "mcp.json")
}

// LoadProjectConfig reads workDir's project-scoped MCP config — same
// shape and not-found/malformed semantics as LoadConfig, just a
// different path.
func LoadProjectConfig(workDir string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(ProjectConfigPath(workDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// Merge combines a user-scoped and a project-scoped config — project
// entries override or add to user ones by server name, the same
// "project overrides user" convention internal/customcmd and
// internal/skill already use for their own two-layer configs.
func Merge(user, project Config) Config {
	merged := Config{MCPServers: make(map[string]ServerConfig, len(user.MCPServers)+len(project.MCPServers))}
	for name, cfg := range user.MCPServers {
		merged.MCPServers[name] = cfg
	}
	for name, cfg := range project.MCPServers {
		merged.MCPServers[name] = cfg
	}
	return merged
}

var trustStore = trust.Open("trusted_mcp.json")

// ContentHash returns a stable identifier for a project mcp.json's raw
// content — see main.go's confirmMCPTrust, the same content-hash-keyed
// one-time-approval hooks and permrules already require, since a
// project-scoped MCP server is exactly as capable of running arbitrary
// commands as a hook is.
func ContentHash(data []byte) string {
	return trust.ContentHash(data)
}

// IsTrusted reports whether hash has already been approved in a past run.
func IsTrusted(hash string) (bool, error) {
	return trustStore.IsTrusted(hash)
}

// Trust records hash as approved, persisting it for future runs.
func Trust(hash string) error {
	return trustStore.Trust(hash)
}
