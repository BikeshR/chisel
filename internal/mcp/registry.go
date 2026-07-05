package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// LoadAndStartAll starts every server in cfg concurrently — sequentially,
// one server's handshake (which can mean an npx cold download) blocked
// every other server's, and the whole app launch behind all of them;
// started in parallel, the wait is bounded by the slowest single server
// instead of their sum. A server that fails to start is not fatal to
// chisel as a whole — its error is returned alongside a Registry holding
// whatever did start, so the rest of the tool set still works.
//
// Takes an already-loaded Config rather than reading one itself — cfg is
// typically LoadConfig's user-scoped result merged with
// LoadProjectConfig's project-scoped one (see Merge), and the caller
// (main.go) needs to apply a trust gate to the project half before
// anything in it is allowed to start, the same reasoning hooks/permrules
// already gate their own project-scoped config before use.
func LoadAndStartAll(cfg Config) (*Registry, []error) {
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}

	type startResult struct {
		name string
		s    *Server
		err  error
	}
	// Each goroutine writes only to its own index — no shared mutable
	// state, so no mutex is needed here.
	results := make([]startResult, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		// SplitToolName finds the server/tool boundary by cutting at the
		// *first* "__" in mcp__<server>__<tool> — a server name that
		// itself contains "__" (e.g. "my__server") would make that cut
		// land in the wrong place and misroute every call to it, so
		// reject the name here rather than starting a server whose tools
		// can never be dispatched correctly.
		if strings.Contains(name, "__") {
			results[i] = startResult{name: name, err: fmt.Errorf("name must not contain \"__\" — it's used as the delimiter between server and tool name in mcp__<server>__<tool>")}
			continue
		}
		wg.Add(1)
		go func(i int, name string, serverCfg ServerConfig) {
			defer wg.Done()
			s, err := Start(name, serverCfg)
			results[i] = startResult{name: name, s: s, err: err}
		}(i, name, cfg.MCPServers[name])
	}
	wg.Wait()

	r := &Registry{servers: make(map[string]*Server, len(names))}
	var errs []error
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", res.name, res.err))
			continue
		}
		r.servers[res.name] = res.s
	}
	return r, errs
}

// AddServer starts one additional MCP server and adds it to the
// registry, alongside whatever LoadAndStartAll already started from
// ~/.chisel/mcp.json — for a server chisel wants to offer automatically
// (see main.go's gopls auto-detection) rather than requiring the user
// to hand-configure it. Refuses to override a server the user already
// configured under the same name: an explicit config always wins over
// an automatic one.
func (r *Registry) AddServer(name string, cfg ServerConfig) error {
	if _, exists := r.servers[name]; exists {
		return fmt.Errorf("mcp server %q is already configured", name)
	}
	s, err := Start(name, cfg)
	if err != nil {
		return err
	}
	if r.servers == nil {
		// A zero-value &Registry{} (rather than one built via
		// LoadAndStartAll, which always initializes this) is a
		// perfectly natural way to construct one just to add a single
		// server — don't require the caller to know about this
		// internal detail to avoid a nil-map panic on the write below.
		r.servers = make(map[string]*Server)
	}
	r.servers[name] = s
	return nil
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

// ResourceRef namespaces one server's resource with which server it
// came from, for display and for RegistryReadResource's dispatch.
type ResourceRef struct {
	Server      string
	URI         string
	Name        string
	Description string
}

// Resources returns every resource from every running server that
// actually implements resources/list — most don't, and that's not an
// error (see Server.listResources' own doc comment), it's just an
// empty contribution to this list. Sorted by server then URI for the
// same stable-ordering reason Tools() sorts.
func (r *Registry) Resources() []ResourceRef {
	var refs []ResourceRef
	for name, s := range r.servers {
		for _, res := range s.Resources() {
			refs = append(refs, ResourceRef{Server: name, URI: res.URI, Name: res.Name, Description: res.Description})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Server != refs[j].Server {
			return refs[i].Server < refs[j].Server
		}
		return refs[i].URI < refs[j].URI
	})
	return refs
}

// ReadResource fetches uri from serverName.
func (r *Registry) ReadResource(ctx context.Context, serverName, uri string) (string, error) {
	s, ok := r.servers[serverName]
	if !ok {
		return "", fmt.Errorf("mcp server %q is not running", serverName)
	}
	return s.ReadResource(ctx, uri)
}

// PromptRef namespaces one server's prompt the same way ResourceRef does.
type PromptRef struct {
	Server      string
	Name        string
	Description string
	Arguments   []PromptArgument
}

// Prompts returns every prompt from every running server that actually
// implements prompts/list — same "most don't, that's fine" caveat as
// Resources.
func (r *Registry) Prompts() []PromptRef {
	var refs []PromptRef
	for name, s := range r.servers {
		for _, p := range s.Prompts() {
			refs = append(refs, PromptRef{Server: name, Name: p.Name, Description: p.Description, Arguments: p.Arguments})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Server != refs[j].Server {
			return refs[i].Server < refs[j].Server
		}
		return refs[i].Name < refs[j].Name
	})
	return refs
}

// GetPrompt fetches and expands name from serverName with arguments.
func (r *Registry) GetPrompt(ctx context.Context, serverName, name string, arguments map[string]string) (string, error) {
	s, ok := r.servers[serverName]
	if !ok {
		return "", fmt.Errorf("mcp server %q is not running", serverName)
	}
	return s.GetPrompt(ctx, name, arguments)
}

// ServerStatus is a snapshot of one configured server's health, for
// display (see /status in the tui package).
type ServerStatus struct {
	Name   string
	Broken bool
}

// Statuses returns every running server's name and current health,
// sorted by name for a stable display order. Safe to call on a nil
// *Registry (reports no servers) — a test-constructed tui.Model won't
// always have gone through tui.New, which is the only real caller that
// guarantees a non-nil one.
func (r *Registry) Statuses() []ServerStatus {
	if r == nil {
		return nil
	}
	out := make([]ServerStatus, 0, len(r.servers))
	for name, s := range r.servers {
		out = append(out, ServerStatus{Name: name, Broken: s.broken.Load()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
