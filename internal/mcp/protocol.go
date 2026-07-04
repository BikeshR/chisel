package mcp

import "encoding/json"

// protocolVersion is what chisel declares during initialize. Per the MCP
// spec's negotiation, the server may respond with a different version it
// supports instead — chisel accepts whatever comes back rather than
// hard-failing on a mismatch, since this is a personal tool talking to
// whatever server the user configured, not a strict conformance client.
const protocolVersion = "2025-06-18"

// request is a JSON-RPC 2.0 request. Notifications reuse this shape with
// ID omitted (encoding/json drops it via omitempty).
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response. Exactly one of Result/Error is set
// on any given response that carries an ID.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// Tool is one tool an MCP server exposes, in the server's own (unprefixed)
// naming — see Registry for the mcp__server__tool naming chisel presents
// to the model.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolsCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}
