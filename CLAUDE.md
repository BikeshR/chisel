# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

chisel is a personal terminal coding agent: a Bubbletea TUI wrapped around
OpenCode Go's OpenAI-compatible chat-completions API, with a small fixed
tool set (`bash`, file editing, `glob`, `grep`), extensible with MCP
servers (`internal/mcp`). Single provider, no SDK dependency — chisel owns
its own HTTP client and SSE decoder. See `docs/design.md` for the full
architecture rationale and roadmap, including *why* it ended up
single-provider and SDK-free (it didn't start that way).

## Commands

```sh
make build            # go build -o chisel .
make test             # unit tests, no network — go test ./...
make integration-test  # live tests against the real API — needs CHISEL_API_KEY
make fmt               # gofmt -l . (fails if anything is unformatted)
make vet               # go vet ./...
make lint              # golangci-lint run ./... — install separately, see golangci-lint.run
make check             # fmt + vet + lint + test, in that order
make install            # go install . — puts chisel on $(go env GOPATH)/bin
```

Run a single test: `go test ./internal/agent/ -run TestNeedsPermission -v`.

Integration tests live in `internal/agent/integration_test.go` behind a
`//go:build integration` tag — excluded from `go test ./...` and from CI by
default. They hit the real OpenCode Go API and cost real usage against its
subscription caps, so they're a manual/local check, not automated.

Config for local runs: `~/.chisel.env` (outside the repo, gitignored by
being outside it entirely) sets `CHISEL_API_KEY`, `CHISEL_BASE_URL`
(defaults to `https://opencode.ai/zen/go` if unset), and `CHISEL_MODEL`.
Real environment variables always take precedence over the file.

## Architecture

**Package boundary: `internal/agent` owns every wire-format concern;
`internal/tui` never touches HTTP or JSON.** `agent.Client.SendStreaming`
builds the request, POSTs it, and decodes the SSE response itself
(`client.go`'s `decodeStream`) — no provider SDK anywhere in the module.
The TUI only ever sees `agent.Message`, `agent.ToolCall`, and `agent.Event`
values over a channel. If you're adding a provider-specific behavior, it
belongs in `internal/agent`; if you're adding UI/interaction behavior, it
belongs in `internal/tui`.

**Tool declaration and tool execution are two separate things, correlated
only by name.** `agent.Tool`/`ToolFunction` (in `custom_tools.go` for
`bash`/`str_replace_based_edit_tool`, `search.go` for `glob`/`grep`) is
what gets sent to the model as a schema. `agent.Execute` (`tools.go`)
dispatches on `call.Function.Name` to `runBash`/`runEditor`/`runGlob`/
`runGrep`. There's no registration mechanism tying a schema to its
handler — matching names is the only contract, so renaming a tool means
updating both the declaration and the `switch` in `Execute`,
`NeedsPermission`, and `Summarize`.

**The agent loop is a Bubbletea Elm-architecture state machine, not a
literal loop.** `internal/tui/model.go` defines the state enum
(`stateInput → stateWaitingModel → [stateAwaitingPermission] →
stateExecutingTool → …`); `update.go`'s `Update` advances it one message at
a time. Streaming works by a `tea.Cmd` re-arming itself: `stream.go`'s
`waitForChunk` reads one `agent.Event` off the channel and returns; if it
wasn't the final event, `handleStreamEvent` in `update.go` calls
`waitForChunk` again to keep listening. Tool calls in one assistant turn
are processed **sequentially, one at a time** (`dispatchNextTool` pops the
front of `pendingUses`), not concurrently — this keeps the permission-gate
UX simple (one y/n prompt at a time) at the cost of throughput.

**Every filesystem tool resolves paths through
`agent.resolveInWorkDir`.** It rejects absolute paths outside the working
directory, `..` traversal, and symlinks that resolve outside it. Any new
filesystem-touching tool must go through this, not `os.Open`/`os.Create`
directly.

**Permission gating is name- and command-based, not path-based.**
`agent.NeedsPermission` (`tools.go`) hardcodes: `bash` always needs
confirmation; `str_replace_based_edit_tool` needs it unless the parsed
`command` field is `"view"`; everything else (`glob`, `grep`) is
auto-allowed. It inspects the tool call's arguments directly, not the
result of running it. `internal/tui/model.go`'s `needsPermission` wraps
this with one more rule before delegating: any `mcp__`-prefixed call
always needs permission, since chisel has no way to know what an
arbitrary MCP server's tool actually does — that rule lives in `tui`, not
`agent` (see below for why).

**`internal/mcp` doesn't import `internal/agent`, on purpose.** It would
be the natural place to return `[]agent.Tool` directly from
`Registry.Tools()`, but `agent.Execute` routing calls to MCP servers
would then need to import `mcp` back — a cycle. Instead `mcp` defines its
own `Tool` type and stays a standalone protocol client; `main.go` converts
`mcp.Tool` → `agent.Tool` once, and `tui/model.go`'s `executeTool` checks
`mcp.IsToolName` to route a call to `mcp.Registry.Call` instead of
`agent.Execute`. The practical effect: MCP-specific behavior (the
always-permission rule above, the `mcp__server__tool` → `"server: tool"`
prompt rendering in `summarizeCall`) lives in `tui`, not `agent` — if
you're about to add an MCP special-case inside `agent`, it likely belongs
in `tui` instead.

**Model-specific quirks get handled at the render layer, not the decode
layer.** `decodeStream` accumulates exactly what the model sends, verbatim.
Cosmetic differences between models — e.g. MiniMax emitting its reasoning
inline as `<think>...</think>` in the content text rather than a separate
field — are handled in `internal/tui/think.go`'s `renderAssistantText`,
which re-scans the full accumulated text on every render rather than
tracking incremental parse state (so a tag split across two streamed
chunks is handled for free, at the cost of re-scanning on every delta).

**Two different token counters answer two different questions — don't
merge them.** `Model.tokensIn`/`tokensOut` (`update.go`) are a running sum
across every request this session, for cost tracking. `Model.
lastContextTokens` is just the most recent request's prompt size — "how
full is the context window right now" — since every request resends the
full history, summing across turns would double-count and isn't the
number `/compact`'s warning threshold (`contextWarnThreshold`, `model.go`)
should ever compare against.

**Plan mode is enforced twice, at two different layers, and both matter.**
`agent.Client.planMode` (`client.go`) only changes what the model is
*told* — an extra system-prompt instruction to explore and propose rather
than act. The actual guarantee is separate: `dispatchNextTool`
(`internal/tui/update.go`) checks `m.client.PlanMode()` and hard-denies
any call that would otherwise need permission, before it ever reaches the
y/n prompt or `agent.Execute`. Don't rely on the system-prompt instruction
alone for anything safety-relevant — a model can ignore it; it can't
bypass the dispatch-layer check.

**A subagent is just a slower tool call, not a new TUI state.**
`dispatch_subagent` (`subagent.go`) runs `RunSubagent`'s own small,
synchronous send/execute loop entirely inside the single `tea.Cmd` that
`executeTool` already returns for any tool — no new Bubbletea state was
needed. Its safety property comes from the same trick plan mode uses for
mutating calls, pushed one step further: rather than denying calls at
dispatch time, `subagentTools()` is a tool list that's *incapable* of
mutating anything — glob, grep, and `view` (a read-only-only variant of
the editor tool, with no create/str_replace/insert command in the schema
at all) — so a subagent needs no permission gate in the first place;
there's nothing to gate.
