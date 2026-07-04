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
directly — `grep`/`glob` (`search.go`) originally didn't, and opened
`filepath.WalkDir`/`doublestar.Glob` matches directly, so a symlink
inside the working directory pointing outside it was followed silently
with no permission prompt. Both now filter every candidate path through
`resolveInWorkDir` first. If you're writing a new tool that touches
files by any path chisel didn't construct itself, assume it needs the
same check.

**Permission gating is name- and command-based, not path-based, and
centralized behind one decision function.** `agent.NeedsPermission`
(`tools.go`) hardcodes: `bash` always needs confirmation;
`str_replace_based_edit_tool` needs it unless the parsed `command` field
is `"view"`; everything else (`glob`, `grep`) is auto-allowed. It
inspects the tool call's arguments directly, not the result of running
it. `internal/tui/permission.go`'s `decidePermission` is the one place
that turns this (plus the MCP-always-asks rule, plus plan mode's
hard-deny, plus the session allowlist from "always allow") into a single
`allow | ask | deny(reason)` outcome — this used to be four independent
checks scattered across `tui` with three different rendering styles for
"this was refused," found and unified in a repo-wide review. preToolUse
hooks are deliberately *not* part of this decision function even
though they can also block a call: a hook is an arbitrary shell command
that can take real time, so it has to run inside the same async `Cmd`
that executes the tool call (`executeTool`, `model.go`), not a
synchronous check made before the permission prompt appears the way the
rest of this decision is. If you're adding a new reason a call might not
run, it almost certainly belongs in `decidePermission`, not as a fifth
scattered check.

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

**The transcript stores raw entries, not pre-rendered strings — render
at display time, not append time.** `internal/tui/transcript.go`'s
`entry` type holds either an already-styled string (most lines) or, for
assistant text specifically, the raw unstyled content plus a flag —
`entry.render(showThinking)` decides collapsed-vs-expanded on every
call, and `Model.transcriptContent()` re-wraps every entry to the
current terminal width on every call, both from the same stored data.
This replaced a `[]string` where styling and wrapping were baked in the
moment a line was appended, which made re-wrapping on resize and
`/think` re-rendering *past* messages both impossible — there was
nothing left to re-derive them from. If you're adding a new kind of
transcript line, give it an `entry` (styled string, or raw+isAssistant),
not a pre-rendered one appended straight into a slice — anything
rendered once and frozen at append time will hit the same wall the next
time the display needs to react to something (a resize, a toggle) after
the fact.

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

**A goroutine spawned inside a timeout-bounded call must not touch the
owning struct's fields after the call returns — capture what it needs as
locals first.** `BashSession.run` and `mcp.Server.call` both spawn a
goroutine to do a blocking read while the caller waits on a `select`
against a timeout. On timeout, the caller returns *before* that goroutine
finishes — it's still out there, still running. If it reads `s.reader`/
`s.marker` (or equivalent) as live struct fields rather than local
copies taken before the goroutine started, a concurrent `stop()`/
`start()` (from the *next* call) racing with it can nil-deref it or hand
it the next session's data. Both now snapshot what the goroutine needs
into locals before spawning it.

**`exec.Cmd.Wait` has exactly one legal caller, ever — plan for that up
front.** It's documented as unsafe to call from two goroutines, and
calling it twice on the same `Cmd` after the first call already
completed is itself an error. `mcp.Server.markBroken` (on a timeout)
kills the process but *deliberately does not* call `Wait` on it, even
though that leaves a zombie until `Close` (called once, at shutdown)
reaps it later — the first version of this fix spawned a background
`Wait()` in `markBroken` "to reap it promptly," and that raced with
`Close`'s own `Wait()` under `-race`. If you need eager reaping,
restructure so only one path ever owns the `Wait` call — don't add a
second one relying on timing.

**Hooks are project-scoped; everything else config-like is user-scoped —
that split is intentional, not an inconsistency.** `internal/hooks` reads
`.chisel/hooks.json` from the *working directory*, unlike `~/.chisel.env`
and `~/.chisel/mcp.json`. Which hooks apply is a property of the project,
meant to be committed with the code it lints/guards; API keys and MCP
servers are properties of the person running chisel. Hooks run inside
`executeTool`'s existing `tea.Cmd` (`internal/tui/model.go`) rather than
as a separate pre-permission-prompt check like plan mode's block — a hook
is an arbitrary shell command bounded by a real timeout (`hooks.
hookTimeout`, 30s), not a cheap boolean, so it can't run synchronously on
the Update goroutine the way plan mode's check does. Consequence worth
knowing: a `preToolUse` hook can still block a call after the user already
approved it via the permission prompt — accepted rather than adding a
second async round-trip before every permission decision just to
pre-empt the prompt in the rare case a hook would've blocked it anyway.
