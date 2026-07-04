package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolPrefix names every tool a configured MCP server contributes as
// mcp__<server>__<tool> — namespaced so two servers can't collide with
// each other or with chisel's own fixed tools, and identifiable enough
// that permission and dispatch logic elsewhere can recognize "this call
// came from MCP" from the name alone.
const toolPrefix = "mcp__"

// Registry holds every MCP server that started successfully.
type Registry struct {
	servers map[string]*Server
}

// LoadAndStartAll reads ~/.chisel/mcp.json (if present) and starts every
// configured server. A server that fails to start is not fatal to
// chisel as a whole — its error is returned alongside a Registry holding
// whatever did start, so the rest of the tool set still works.
func LoadAndStartAll() (*Registry, []error) {
	cfg, ok, err := LoadConfig()
	if err != nil {
		return &Registry{servers: map[string]*Server{}}, []error{fmt.Errorf("load mcp config: %w", err)}
	}
	if !ok {
		return &Registry{servers: map[string]*Server{}}, nil
	}

	r := &Registry{servers: make(map[string]*Server, len(cfg.MCPServers))}
	var errs []error
	for name, serverCfg := range cfg.MCPServers {
		s, err := Start(name, serverCfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", name, err))
			continue
		}
		r.servers[name] = s
	}
	return r, errs
}

// Tools returns every tool from every running server, in chisel's
// prefixed naming, as (name, description, inputSchema) triples ready to
// become agent.Tool values — this package deliberately doesn't import
// agent, so it stays a standalone protocol client usable outside chisel.
// Sorted by name: r.servers is a map, whose iteration order Go randomizes
// on purpose, and this list becomes part of every request's bytes — an
// order that changes from call to call would be an unforced way to bust
// any prompt caching keyed on a stable prefix.
func (r *Registry) Tools() []Tool {
	var tools []Tool
	for name, s := range r.servers {
		for _, t := range s.Tools() {
			tools = append(tools, Tool{
				Name:        toolPrefix + name + "__" + t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// IsToolName reports whether name follows chisel's mcp__server__tool
// naming — used to route dispatch and permission checks without this
// package needing to know about agent.ToolCall at all.
func IsToolName(name string) bool {
	return strings.HasPrefix(name, toolPrefix)
}

// Call routes a prefixed tool name to its server and invokes it with the
// server's own unprefixed name.
func (r *Registry) Call(ctx context.Context, prefixedName string, arguments json.RawMessage) (content string, isError bool, err error) {
	serverName, toolName, ok := SplitToolName(prefixedName)
	if !ok {
		return "", true, fmt.Errorf("malformed mcp tool name %q", prefixedName)
	}
	s, ok := r.servers[serverName]
	if !ok {
		return "", true, fmt.Errorf("mcp server %q is not running", serverName)
	}
	return s.CallTool(ctx, toolName, arguments)
}

// SplitToolName reverses the mcp__server__tool naming Tools() produces —
// exported so callers outside this package (chisel's permission-prompt
// rendering) can show a server/tool pair without duplicating the
// convention themselves.
func SplitToolName(prefixedName string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(prefixedName, toolPrefix)
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(rest, "__")
	return server, tool, ok
}

// Close shuts down every running server.
func (r *Registry) Close() {
	for _, s := range r.servers {
		s.Close()
	}
}
