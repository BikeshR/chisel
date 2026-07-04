package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
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
