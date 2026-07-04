# Building chisel: design notes

Personal CLI coding agent. Solo use, started from an empty repo. This document
tracks how the existing tools in this space are built, why chisel is built
the way it is, what it costs to run, and the roadmap from MVP to something
with teeth.

**Status:** Phase 1 (minimum viable agent) is done; Phase 2's streaming is
in. Chisel is **OpenCode Go only** — a single provider, authenticated with
one key, speaking OpenCode Go's OpenAI-compatible `/v1/chat/completions`
endpoint directly. No SDK dependency of any kind; chisel owns its own HTTP
client and SSE decoder. See [Roadmap](#6-roadmap-mvp-to-feature-parity).

## Contents

1. [How the incumbents are built](#1-how-the-incumbents-are-built)
2. [The agent loop, underneath it all](#2-the-agent-loop-underneath-it-all)
3. [What chisel is built with](#3-what-chisel-is-built-with)
4. [Model options & what they cost](#4-model-options--what-they-cost)
5. [Roadmap: MVP to feature parity](#5-roadmap-mvp-to-feature-parity)
6. [Bottom line](#6-bottom-line)

---

## 1. How the incumbents are built

Every serious CLI coding agent on the market converges on the same shape: a
thin terminal front-end wrapped around a loop that alternates between
calling a model and executing tools. The differences are in language choice,
how polished the terminal UI is, and how much ceremony sits around
permissions. None of them are architecturally exotic — which is good news,
because it means chisel doesn't need to solve a hard problem to get off the
ground.

| Tool | Language | TUI layer | Notable architecture |
|---|---|---|---|
| Claude Code | TypeScript | Custom renderer (Ink-family) | One `queryLoop` async generator serves the CLI, SDK, and IDE surfaces alike. Seven graduated permission modes (plan → default → acceptEdits → auto → bypass) with a deny-always-wins precedence rule. Five layered context-shrinking passes run before every model call. |
| OpenAI Codex CLI | Rust | ratatui | Rewritten from TypeScript into Rust for a single static binary and OS-level sandboxing (Seatbelt on macOS, Landlock/seccomp on Linux) instead of a container. |
| Gemini CLI | TypeScript / Node | Ink | Same ReAct-style loop, MCP-first extension model, ships as an open-source npm package. |
| Aider | Python | Plain terminal + prompt_toolkit | Git-native — every accepted edit is an auto-commit. Provider-agnostic via `LiteLLM` rather than a hand-rolled client per API. Uses a ctags-derived repo map instead of stuffing whole files into context. |
| OpenCode | TypeScript | Bubbletea-style TUI | Built on the Vercel AI SDK plus the `models.dev` catalog for 75+ providers behind one interface. |
| Crush (Charm) | Go | Bubbletea | Optimizes hardest for terminal polish; Go + Bubbletea is the dominant pairing when the TUI itself is the product. |
| Goose (Block) | Rust (+ Python bindings) | ratatui | Framed as a general extensible agent rather than coding-specific; leans on MCP for essentially all capability beyond the core loop. |
| Cline | TypeScript | VS Code webview | IDE-embedded rather than standalone; explicit Plan / Act mode split and an MCP marketplace inside the extension. |

Two things worth pulling out of that table. First, **TypeScript is the
plurality choice** among tools built for developer ergonomics and fast
iteration, while **Rust and Go show up specifically where a single
dependency-free binary or a showcase terminal UI is the point**. Second,
**every provider-agnostic tool got there through an existing abstraction**
(LiteLLM, the Vercel AI SDK, models.dev) rather than writing bespoke API
clients per provider. Chisel took the opposite path once it committed to a
single provider — see §3.

## 2. The agent loop, underneath it all

Strip away the TUI chrome and permission ceremony, and Claude Code, Codex
CLI, Aider, and every other tool in the table run the same five-step cycle:

```
loop:
  response = model.send(system_prompt, tools, message_history)

  if response.finish_reason != "tool_calls":
    break              // model is done, show the final text

  for each tool_call in response:
    check_permission(tool_call)     // ask, allow, or deny
    result = execute(tool_call)     // read/write/edit/bash/glob/grep

  message_history.append(response, all tool results)
  // repeat — model sees the results and decides the next step
```

A working version of that loop, against a single model with four or five
tools, is a few hundred lines. Everything past that point — subagents,
hooks, MCP, compaction, sandboxing — is additional structure *around* this
loop, not a different loop.

In chisel this loop is driven by Bubbletea's Elm architecture rather than a
literal `for` loop: each step (one API call, one tool execution, one
permission decision) is a `tea.Cmd` that resolves into a message, and
`Update` advances a small state machine (`stateInput → stateWaitingModel →
[stateAwaitingPermission] → stateExecutingTool → …`) one message at a time.
See `internal/tui/update.go`.

## 3. What chisel is built with

> **Decided: OpenCode Go only.** Chisel talks to exactly one provider —
> OpenCode Go's `/v1/chat/completions` endpoint — with no SDK in between.
> This was a deliberate pivot partway through building chisel; the
> reasoning is worth keeping since it explains choices elsewhere in this
> doc that would otherwise look arbitrary.

**Language and TUI.** Go, with Bubbletea for the TUI, built from day one —
not deferred to a later phase. Bubbletea's Elm architecture (one state
struct, a pure `update` function, a pure `view` function) is a genuinely
better fit for an interactive agent loop with streaming tool output than
bolting a TUI onto a loop that wasn't designed with one in mind. Paired with
`Lipgloss` for styling and `Bubbles` for the common components
(`textinput`, `viewport`, `spinner`).

**No SDK, by choice.** Chisel originally used `anthropic-sdk-go` — first
talking to real Anthropic models, then (once OpenCode Go turned out to
expose an Anthropic-compatible endpoint) pointing that same SDK at OpenCode
Go instead. Once OpenCode Go became the *only* provider chisel would ever
use, keeping a whole SDK around — with its dual-provider branching, its
Anthropic-specific type system, its schema-less built-in tools that don't
even translate through OpenCode's layer (see the Phase 2 notes in §5) — was
carrying complexity with no remaining payoff. `internal/agent/client.go` is
now a plain `net/http` client: build the JSON request body, POST it,
decode the SSE stream by hand. `go.mod` has no dependency on any provider
SDK at all.

**Wire format: OpenAI-shaped, not Anthropic-shaped.** OpenCode Go exposes
both. Anthropic's `/v1/messages` shape was already proven working when the
decision came up (see §5) — genuine risk in switching. The case for
switching anyway: OpenAI's chat-completions shape is the de facto industry
standard (OpenRouter, Groq, Together, Fireworks, most open-weight hosts,
local runners like Ollama/vLLM all speak it as their primary interface),
while Anthropic's shape is a comparatively niche accommodation a handful of
gateways build specifically for Claude-Code-like tools — not the direction
to keep leaning on for a tool that's now committed to the open-weight-model
world. It's also simpler to hand-roll: flatter streaming (no named SSE
event types to track), and no schema-less-tool special case to ever worry
about again, since OpenAI's convention never had that concept — every tool
always needs a schema, which chisel already writes (`custom_tools.go`,
`search.go`).

**Tools.** All four — `bash`, `str_replace_based_edit_tool`, `glob`, `grep`
— are custom tools with hand-written JSON schemas (`internal/agent/
custom_tools.go`, `search.go`). There's no more built-in/custom split by
provider; every provider needs a real schema now, so there's exactly one
code path. Tool-call round-tripping follows OpenAI's convention: the
assistant's tool calls live in a `tool_calls` array on its message, and each
result goes back as its own separate `{role: "tool", tool_call_id,
content}` message — not merged into one message the way Anthropic's
content-block convention would. All filesystem operations resolve through
`agent.resolveInWorkDir`, which rejects any path that would escape the
working directory (absolute paths elsewhere, `..` traversal, or a symlink
pointing outside).

**Streaming.** `agent.Client.SendStreaming` validates the HTTP response
status before ever touching the SSE body — a real improvement over the
SDK-based version, which (as documented in §5) could silently treat a
malformed error response as an empty success. The decoder
(`internal/tui`-agnostic, lives entirely in `internal/agent/client.go`)
accumulates `choices[].delta.content` into a single growing string and
`choices[].delta.tool_calls[].function.arguments` into per-index buffers
(OpenAI's convention allows a tool call's arguments to arrive as partial
JSON fragments across many chunks, keyed by index for parallel calls),
stopping at the literal `data: [DONE]` line and ignoring OpenCode's own
non-standard trailing bookkeeping frames that follow it.

**MCP** (Phase 3, §5) was the highest-leverage roadmap item at the time this
was written — a standard that unlocks an entire ecosystem of tools chisel
doesn't have to build itself, independent of which wire format the model API
uses. Since built; see §5 for what's left.

## 4. Model options & what they cost

Chisel's only provider is OpenCode Go — a $10/month subscription ($5 first
month) with dollar-equivalent usage caps ($12/5hr, $30/week, $60/month),
giving access to a curated set of open-weight models. Pricing is
subscription-based, not metered per-token, so the wire format and model
choice have no separate cost implication beyond which models the
subscription includes.

Confirmed by direct testing against the live catalog (`agent.KnownModels()`,
sourced from OpenCode Go's own `/v1/models` endpoint):

| Model | Status |
|---|---|
| `minimax-m3` | Works — current default, cleanest responses in testing |
| `glm-5.2`, `glm-5.1`, `glm-5` | `glm-5.2` confirmed working |
| `qwen3.7-max`, `qwen3.7-plus`, `qwen3.6-plus`, `qwen3.5-plus` | `qwen3.7-max` confirmed working; emits its own `<think>` reasoning inline in the response text, not a separate field |
| `deepseek-v4-pro`, `deepseek-v4-flash` | `deepseek-v4-flash` confirmed working (2026-07) — initially failed with a generic upstream 400 earlier the same month, resolved on OpenCode's/that backend's side by the time it was re-checked |
| `kimi-k2.7-code`, `kimi-k2.6`, `kimi-k2.5` | `kimi-k2.6` confirmed working (2026-07) — same recovery as deepseek-v4-flash |
| `minimax-m2.7`, `minimax-m2.5` | Untested |
| `mimo-v2-pro`, `mimo-v2-omni`, `mimo-v2.5-pro`, `mimo-v2.5` | Untested |
| `hy3-preview` | Untested |

That deepseek/kimi recovery is itself the reason `/model check` exists now
(see Phase 2 below) rather than this table being the only source of truth
— a static snapshot goes stale exactly like this one did, and there's no
way to know it has without asking the model directly. Switch models
anytime with `/model` (no args lists the catalog with the current one
marked; `/model <id>` switches immediately, mid-session; `/model check
[id]` live-tests one through chisel's real request shape — system prompt
and tools included, so it catches the same class of failure as a genuine
chat turn, not just plain reachability).

**On caching:** OpenCode Go's responses include real `prompt_tokens_details.
cached_tokens` in the usage payload — genuine, automatic caching, no
explicit opt-in needed (unlike Anthropic's `cache_control` marker). Not
something chisel does anything to enable or control; it's inherent to
however OpenCode/the underlying model serves repeated prefixes.

## 5. Roadmap: MVP to feature parity

Four phases, each a genuinely usable tool in its own right.

### Phase 1 — Minimum viable agent — **done**

- The five-step loop above, against a single model
- Core tools: `bash`, `str_replace_based_edit_tool` (view/create/str_replace/insert), `glob`, `grep`
- A basic permission gate — ask before bash or file edits, auto-allow read-only tools
- Bubbletea TUI: scrollable conversation pane, status line (model, token spend), inline approve/deny prompt

### Phase 2 — Daily driver — **done**

- ~~Streamed output instead of waiting for the full response~~ — **done**: hand-rolled SSE decode, text deltas render live via a channel + `tea.Cmd` re-arm loop (`internal/tui/stream.go`)
- ~~A config file for model choice~~ — **done**: `CHISEL_MODEL` env var (switchable at runtime with `/model`), plus an optional `~/.chisel.env` (outside the repo, never committed) for `CHISEL_API_KEY`/`CHISEL_BASE_URL`/`CHISEL_MODEL`. Working-directory scope and an allowed-commands list are still open.
- ~~Model health-check~~ — **done**: `/model check [id]` (`internal/tui/commands.go`) sends a minimal request through chisel's real request shape (system prompt + tools included) and reports pass/fail with the actual reply or error. Caught a real, useful finding immediately: `deepseek-v4-flash` and `kimi-k2.6`, both recorded as failing in §4 earlier in July 2026, now work — confirms this needed to be a live check, not a maintained static list.
- ~~Session persistence~~ — **done**: `internal/session` saves the conversation (`[]agent.Message`) to `~/.chisel/sessions/<sanitized-workdir>.json` after every turn, scoped per working directory so separate projects don't share history. `main.go` loads it back on startup and `tui.New` reconstructs the visible transcript from it (`internal/tui/history.go`), skipping the permission-prompt step since every replayed tool call was already resolved in a past run. Deliberately doesn't restore which model was selected — that's a per-run choice (`CHISEL_MODEL`/default), not part of "the conversation". `/new` abandons the current transcript, in memory and on disk.
- ~~Git awareness~~ — **done**: `agent.PreviewEdit` (`internal/agent/diff.go`) computes a unified diff of what a `str_replace_based_edit_tool` call would change — without writing anything — and `dispatchNextTool` shows it inside the existing permission prompt, so approving an edit is an informed decision rather than trusting the summary line alone. Separately, `internal/gitutil` + `/git auto on|off` (off by default) adds Aider-style auto-commit: after a turn that changed files completes, chisel stages and commits everything with a message derived from the user's request. Deliberately opt-in — silently creating git history isn't something to default to — and refuses to turn on outside a git repo rather than leaving a setting that would just silently no-op.
- ~~Persistent bash session~~ — **done**: `agent.BashSession` (`internal/agent/bashsession.go`) runs one long-lived `sh` process for the life of the TUI session (owned by `main.go`, independent of `/model` switches) instead of a fresh subprocess per call, so `cd` and exported env vars now carry across calls. Completion is detected via a random sentinel line echoed after each command, carrying its exit code; a 2-minute per-command timeout kills and drops the session (recreated lazily on the next call) rather than risking a desynced read. `{"restart": true}` (already part of the tool's schema) now does something real: kills the shell and starts a fresh one rooted back at workDir.

**How chisel got here — history worth keeping:**

- Chisel briefly supported *two* providers at once (real Anthropic + OpenCode Go, routed by a model-string prefix), before the decision in §3 to drop Anthropic entirely and go OpenCode-Go-only. That dual-provider code, and the SDK it depended on, no longer exists.
- **While still on the Anthropic-compatible endpoint**, direct bisection testing found that Anthropic's built-in `bash`/`text_editor` tools (schema-less by design — Anthropic handles them specially server-side) don't work through OpenCode's translation layer at all: a bare request with no tools succeeded on working models, but adding either built-in tool made those same models fail with a generic upstream 400. A custom tool with a real JSON schema worked fine. This is *why* chisel writes its own schemas for every tool now rather than leaning on any provider's built-ins — it's not just an OpenAI-format requirement, it was already true against OpenCode's Anthropic-compatible endpoint too.
- **Also found on that endpoint:** a real bug, independent of OpenCode — the Anthropic SDK's SSE decoder didn't recognize a plain `application/json` error response (as opposed to a proper `text/event-stream` error event) as a failure at all; it silently reported zero events and a `nil` error, which chisel was treating as an empty success. The rewrite in §3 fixes this more robustly by checking the HTTP status code directly before ever starting to decode the stream — a non-200 response is now always a clean, immediate error.
- **Base URL path gotcha** (kept in case it recurs): whichever client library or hand-rolled request builder you're using, don't include `/v1` in a configured base URL if the client appends `/v1/...` itself — produces a silently-wrong double path and a 404 rather than an obvious error.
- **A focused bug-fix pass** (2026-07) found and fixed 14 issues across the codebase built up through the phases above — worth keeping as a category list, since several are the kind of thing that's easy to reintroduce elsewhere:
  - *Sandbox escapes*: `grep`/`glob` opened matched files directly instead of through `resolveInWorkDir`, so a symlink inside the working directory pointing outside it (`link -> ~/.ssh/id_ed25519`) was followed silently, with no permission prompt — the exact escape `resolveInWorkDir` exists to prevent, just not applied to every tool that touches the filesystem.
  - *Session corruption*: `handleStreamComplete` saved the session immediately after appending an assistant message with tool_calls, before they were resolved — quitting mid-permission-prompt, or a provider reporting `finish_reason: "stop"` alongside a non-empty `tool_calls` array (which also caused those calls to never be dispatched at all), both left a saved session with a dangling unresolved tool call that got every future request against it rejected by the API. Fixed by not saving until a turn is actually fully resolved, and by keying off whether the message has tool calls rather than trusting `finish_reason`.
  - *State loss*: `/model <name>` reconstructed the whole `agent.Client` from scratch, silently dropping MCP tools, plan mode, and memory content that had been configured onto it. `Client.SetModel` now switches just the model in place.
  - *Data races*: `BashSession`'s timeout path returned while its reader goroutine was still running, and that goroutine read `s.reader`/`s.marker` as live struct fields rather than local copies — a concurrent `stop()`/`start()` from the *next* call could nil-deref it or hand it the new session's output. `mcp.Server`'s own timeout fix went through the same lesson twice: the first attempt spawned a background `cmd.Wait()` to reap the killed process, which then raced with `Close()`'s own `Wait()` — `exec.Cmd.Wait` isn't safe to call from two goroutines even sequentially-looking ones, and there can only be one caller of it, ever.
  - *Not actually atomic / not actually bounded*: session saves used a plain `os.WriteFile`, truncating the old file before the new content was fully written — a crash mid-write left a corrupt session; now written to a temp file and renamed into place. The HTTP client's `Timeout` field bounded the *entire* request including reading a streaming body, not just getting one started, so a long but healthy turn could be aborted for no real reason — replaced with a `Transport.ResponseHeaderTimeout`, which bounds only "never got a response at all."
  - *Wrong blast radius*: `/git auto` ran `git add -A`, which stages the user's own unrelated unstaged work sitting in the same tree, not just what chisel changed. Fixed by snapshotting `git status` at the start of each turn and only adding paths that are newly dirty since then.
  - *Small correctness/robustness things*: `mcp.Registry.Tools()` iterated a map (Go randomizes map order on purpose) into the tools array sent on every request, now sorted; three separate "drain the event channel, check only `Err`, then dereference `.Message`" call sites relied on `decodeStream` always sending a final event as an unenforced convention, now centralized behind `agent.Drain`, which checks explicitly; a few `s[:n]`-style byte truncations could split a multi-byte UTF-8 character mid-sequence, now rune-safe; the "was this tool result an error" marker folded into tool-message content was the plain phrase `"Error: "`, which genuine tool output can start with by coincidence (a bash command's own stdout, a matched line) — resuming a session could then misrender a real success as a failure, now a much lower-collision marker.
- **A Fable-model review of the whole repo** (2026-07) — code, architecture, and specifically the terminal UX, which had had much less scrutiny than the agent layer — drove a much larger pass than the bug-fix one above: roughly 16 items, most of Phase 3's remaining list plus several correctness fixes, landed as one commit each over a single sitting. Worth keeping as a category list for the same reason:
  - *Terminal UX, the load-bearing foundation first*: the transcript couldn't scroll at all (no key or mouse event ever reached the viewport, and alt-screen mode disables native scrollback too) and long lines were silently truncated rather than wrapped (`bubbles`' viewport hard-truncates; chisel never wrapped before handing it content). Both traced back to `Model.lines []string` baking styles in at append time — `internal/tui/transcript.go`'s `[]entry` (raw content, rendered fresh on every viewport refresh) is what actually unblocked wrapping-on-resize and scrolling, not either fix in isolation. Also: esc-to-interrupt (nothing before this could stop a runaway request except quitting the whole app), a switch from single-line `textinput` to multi-line `textarea`, colorized/capped diffs, and prompt queueing (typing while busy used to be entirely swallowed).
  - *Permission-model fragmentation*: `agent.NeedsPermission`, the MCP always-ask rule, plan mode's hard-deny, and hooks' preToolUse block each independently decided "don't run this," with three different rendering styles and one outright duplicate line for plan-mode blocks. Centralized into one `decidePermission` returning allow/ask/deny — hooks stay async by design (an arbitrary shell command needs the same async `Cmd` the tool call itself uses), but every refusal now renders through the same path. Same pass added "always allow this session" (`y/n/a`), fixed enter-doubling-as-approve (a real hazard: enter to submit the message, then a reflexive second enter approving whatever it triggered), and made denial resume-able with a reason instead of an immediate canned round-trip.
  - *Security*: project-scoped `.chisel/hooks.json` ran on the very first matching tool call — including auto-allowed ones like glob/grep — with zero confirmation; a hostile cloned repo's hooks could execute merely by being asked "what does this project do?" Now gated by a one-time trust prompt, keyed by a hash of the hooks file's content rather than its path (a hostile *edit* to already-trusted hooks needs re-approval; the same content re-checked out somewhere else doesn't).
  - *Concurrency bugs caught mid-implementation, not just mid-review*: adding a read-idle timeout to `decodeStream` (protection against a body that stops sending data without ever closing) initially had the new watchdog goroutine and the main loop both touching the same `bufio.Scanner` concurrently — caught by `go test -race`, not code review — fixed by having only the goroutine that owns the scanner ever read its error, and giving it a `stop` channel so it can always exit instead of blocking on a channel send nobody's listening to anymore.
  - *Everything else*: MCP servers now start concurrently (total launch time bounded by the slowest one, not their sum) and a server going broken mid-session drops its tools from the client and shows up in the status bar, instead of the model quietly retrying a dead server forever; `RunSubagent`'s loop was generalized into `agent.RunLoop`, which headless mode (`chisel -p`, promoted ahead of checkpoint/rewind, which was demoted the same pass) now reuses directly instead of duplicating; `@file` references inject a file's content client-side with tab completion; `/help`, `/status`, `/retry` fill out command discoverability; plus a long tail of smaller fixes — `/compact` trusting an empty or tool-call-shaped response and wiping the conversation with nothing, bash's timeout killing only the shell process and orphaning anything it had backgrounded, session filenames colliding across different directory structures, and several more in the same vein as the earlier 14-bug pass.

### Phase 3 — Real capability — **done**

- ~~MCP client support~~ — **done**: `internal/mcp` is a stdio-transport client — spawns each configured server, does the `initialize`/`notifications/initialized` handshake, and calls `tools/list` — for servers listed in `~/.chisel/mcp.json` (same `mcpServers` shape as Claude Desktop/Claude Code, so an existing config works unchanged). A server that fails to start isn't fatal to chisel as a whole; `LoadAndStartAll` returns whatever did start alongside the errors for the rest, logged to stderr.

  Tools are namespaced `mcp__<server>__<tool>` (`Registry.Tools`/`Registry.Call`) so two servers, or a server and chisel's own fixed tools, can't collide. `internal/mcp` deliberately doesn't import `internal/agent` — it stays a standalone protocol client with its own `Tool` type; converting to `agent.Tool` happens once, in `main.go`, to avoid a would-be import cycle (`agent.Execute` routing MCP calls would need `mcp`, and `mcp.Tools()` returning `agent.Tool` would need `agent`). For the same reason, `agent.NeedsPermission`/`agent.Summarize` don't know about MCP at all — chisel always requires permission for any `mcp__`-prefixed call (it can't know what an arbitrary server's tool does, so none of the built-in read-only auto-allow heuristics apply) and prettifies the prompt into "server: tool", both living in `internal/tui` (`needsPermission`/`summarizeCall` in `model.go`) as a thin layer on top of the `agent` functions, not a change to them.

  A hung `tools/call` is bounded by the same defensive pattern as the persistent bash session: a timeout marks the connection broken, and every further call to that server fails fast with a "restart chisel to reconnect" message rather than risking a read desynced from a still-pending request — there's no automatic reconnect in this version. Tested with a real subprocess throughout (`internal/mcp/server_test.go` re-execs the test binary itself as a minimal fake MCP server, the standard Go pattern for testing exec-based clients), not mocked at the transport layer.
- ~~Context management~~ — **done**: `/compact` (`internal/tui/model.go`/`commands.go`) sends the conversation so far plus an instruction to summarize it as one more turn through the same client, then replaces `m.messages` with a single message carrying that summary — the model doing its own compaction, since there's no server-side compaction to lean on here (that was an Anthropic API feature). The status bar now shows the *current* request's prompt size (`lastContextTokens`, from the most recent turn's usage) separately from the running cumulative spend total (`tokensIn`/`tokensOut`) — those answer different questions ("how full is the context window right now" vs "what has this session cost so far") and conflating them into one running sum was arguably a small bug in the original token-tracking design. Past a conservative, deliberately generic 100k-token threshold the status bar suggests `/compact` — chisel doesn't maintain a per-model context-window table (the OpenCode Go catalog changes, and getting a specific model's exact limit wrong would be worse than not claiming one at all).
- ~~Plan mode~~ — **done**: `/plan` (`internal/tui/commands.go`) toggles `agent.Client.planMode`, which appends an instruction to the system prompt (`planModeNote`, `client.go`) telling the model to only explore and present a plan. The instruction alone isn't the actual guarantee, though — `dispatchNextTool` (`update.go`) hard-denies any call that would otherwise need permission while plan mode is on, before it ever reaches the y/n prompt or `agent.Execute`, so a model that ignores the instruction can't actually do anything, it just wastes a turn finding out and gets told why. Read-only tools (glob, grep, editor view) are unaffected — that's the whole point of "read-only planning". Verified live: asked to make an edit while in plan mode, `minimax-m3` correctly opened with a `glob` call to explore rather than attempting the edit directly.
- ~~Subagents~~ — **done**: a new `dispatch_subagent` tool (`internal/agent/subagent.go`) the main model can call with a self-contained `task` description. `RunSubagent` runs its own small, synchronous agent loop (send → execute tool calls directly → repeat, up to `maxSubagentTurns`) entirely inside the parent's single `tea.Cmd` — no new Bubbletea states needed, since from the TUI's perspective a subagent is just a slower tool call. The subagent's tool set (`subagentTools`) is glob, grep, and a new `view` tool (a read-only-only variant of the editor tool — no create/str_replace/insert command exists at all, not just discouraged) — deliberately incapable of mutating anything *by construction*, because a subagent runs with no permission gate at all; there was no sane way to give it a nested y/n prompt. Verified live: asked to find a specific type in the codebase, `RunSubagent` returned an accurate, correctly-cited summary (right file, right behavior) without ever touching disk.
- ~~Hooks~~ — **done**: `internal/hooks` reads `.chisel/hooks.json` in the *working directory*, not `~/.chisel/` like MCP servers/env config — which hooks apply is a property of the project ("this repo runs gofmt after edits"), not the person running chisel, and the file is meant to be committed. Two kinds: `preToolUse` hooks can block a call outright (a non-zero exit blocks it, and the hook's own stderr/stdout becomes the reason the model is told — "block writes to certain paths" is a hook checking `$CHISEL_HOOK_PATH` against a pattern); `postToolUse` hooks run after a *successful* call and whatever they print gets folded into the result the model sees ("run a linter after every edit"). Both are matched by tool name (or `"*"` for every call) and get the call's tool name/raw JSON arguments/path (if the call has one) as environment variables, so a simple path check needs no JSON parsing at all.

  Hooks run inside the same `tea.Cmd` that already executes the tool call (`executeTool`, `internal/tui/model.go`), not as a separate pre-permission-prompt step like plan mode's block — a hook is an arbitrary shell command that can take real time (bounded at 30s), unlike plan mode's plain boolean check, which is cheap enough to run synchronously before the y/n prompt even appears. The tradeoff, accepted for simplicity: a preToolUse hook can still block a call the user already approved, rather than pre-empting the permission prompt entirely.
- ~~TUI polish~~ — **done** (the load-bearing half of it): a Fable-model review of the whole repo (code, architecture, and specifically the terminal UX) found the conversation/agent layer was in good shape but the terminal layer itself was still "scaffolding" — no scrolling, no line wrapping, no way to interrupt, single-line input. In dependency order: `internal/tui/transcript.go` replaced `Model.lines []string` (styles baked in at append time) with `[]entry` (raw content, rendered on demand), which is what made the rest possible — wrapping to the current terminal width now happens fresh on every render instead of being frozen at append time, and `/think` can finally re-render *past* messages, not just new ones. On top of that: PgUp/PgDn/ctrl+u/d scrolling (`viewport.Update` was never wired to any key or mouse event before), a switch from `textinput` to `textarea` for multi-line composition (alt+enter for a literal newline; plain enter still submits), and colorized/capped diffs in the permission prompt. Split panes and Lipgloss theming are still open — lower-value polish on top of a now-solid foundation, not blocking anything else.
- ~~Memory file~~ — **done**: `internal/memory` loads `~/.chisel/CHISEL.md` (personal preferences, the base layer) and `<workDir>/CHISEL.md` (project-specific, layered on top) and hands the combined text to `agent.Client.SetMemory`, appended to the system prompt. Both optional; a read error on either is treated the same as `~/.chisel.env` — silently skipped, not a startup failure. A one-line startup banner in the transcript ("loaded CHISEL.md (…)") confirms which were found, since the content itself is invisible once it's just part of the system prompt.
- Custom slash commands — user-defined prompt files (per-project plus `~/.chisel/commands/`) that expand into a canned prompt; Claude Code does this with Markdown files, Gemini CLI with TOML that can also splice in `@file` contents and `!{shell}` output. Still open — the review that drove most of Phase 3's remaining work didn't flag it as urgent, and a project-scoped version should go through the same folder-trust gate hooks now use, once it's built
- Checkpoint/rewind — snapshot files before each turn in a shadow git repo and restore code and conversation together (Gemini's `/restore`, Claude Code's `/rewind`). Not the same thing as `/git auto`: rewind must never write to the project's real history. Explicitly *demoted* by the same review: `/git auto` plus the permission prompt's diff preview already cover most of its value for a solo user in a real git repo, and the shadow-repo machinery is the most complex item left on this list
- ~~`@file` references~~ — **done**: typing `@path/to/file` anywhere in a message injects the file's content before the request is sent (`internal/tui/fileref.go`'s `expandFileReferences`), wrapped with its path so the model can tell where one injected file ends and the next begins. A token that doesn't resolve to a real file (a typo, or just prose — "ask @someone about this") is left as literal text rather than erroring the whole submission. The transcript still shows exactly what was typed, not the expansion, so referencing a large file doesn't turn the display into a wall of text. Reads go through a new `agent.ReadFileInWorkDir`, reusing `resolveInWorkDir`'s validation — this is a new filesystem-touching feature, and CLAUDE.md is explicit that any new one needs that same check. Tab completes the last `@`-token against files under the working directory, shell-style (single match completes fully; several complete to their longest common prefix).
- ~~Prompt queueing~~ — **done**: typing while the model is streaming used to be completely swallowed — every keystroke except esc was ignored. The textarea now stays live while busy; enter there queues the message instead of submitting (there's no turn to submit into yet), and it's delivered in order once chisel next returns to an idle input state, whether that's a turn finishing normally, failing, or being interrupted.
- ~~Notify when idle~~ — **done**: a bell plus an OSC 9 desktop notification (silently ignored by terminals that don't support it) whenever chisel stops needing the model's or a tool's attention and starts needing the user's — a permission prompt appearing, or a turn finishing without one queued to run immediately after. Skipped specifically for an esc-triggered interruption, since that means the user is already at the keyboard.
- Todo list — a model-maintained task checklist tool rendered live in the TUI (Claude Code's TodoWrite/Task tools); as much a steering aid for the model as a progress display for the user. Still open, and reasonably last in line: with tool dispatch already strictly sequential and no long-horizon multi-part tasks yet, it's a steering aid without a steering problem to solve today

### Phase 4 — Parity-and-beyond

- Sandboxing the bash tool (container or OS-level sandbox) — non-negotiable once there's an auto-approve/"just do it" mode
- A second provider, if one ever genuinely earns its way in — would mean reintroducing a thin `Provider` interface, deliberately not built speculatively this time either
- User-defined skill files chisel loads on demand
- Background/async task execution with completion notifications
- Lightweight IDE integration, if it turns out to matter for daily use
- ~~Headless mode~~ — **done**, promoted ahead of checkpoint/rewind by the same review that demoted it: `chisel -p "prompt"` (`main.go`'s `runHeadlessCore`) sends one prompt non-interactively and prints the model's final answer to stdout, exiting non-zero on failure. Restricted to `agent.ReadOnlyTools` (glob, grep, view) — no bash, no edits, no MCP, and hooks aren't loaded — since there's no terminal to show a permission prompt to, so nothing offered can need one; the same guarantee a subagent's tool set already gives. `RunSubagent`'s own send/dispatch loop was generalized into `agent.RunLoop(ctx, client, history, maxTurns, execTool)` for this — headless mode calls it directly with its own client and dispatch function, and `RunSubagent` itself is now a thin wrapper (build a client with `subagentTools`, call `RunLoop`) rather than a second copy of the same loop.
- Image input — attach a screenshot or design spec to a message (Codex and Claude Code both take pasted images); gated on whether OpenCode Go's catalog actually serves a vision-capable model (`mimo-v2-omni` suggests maybe)

## 6. Bottom line

- **Provider:** OpenCode Go, exclusively. One API key (`CHISEL_API_KEY`), OpenAI-compatible `/v1/chat/completions`, no SDK dependency.
- **Stack:** Go, hand-rolled HTTP/SSE client, Bubbletea + Lipgloss + Bubbles for the TUI — built from day one, not deferred.
- **Model:** `minimax-m3` as the default (confirmed working); switch with `/model`.
- **Highest-leverage next investment:** Phase 3 is done. What's left there — custom slash commands, checkpoint/rewind (demoted), a todo-list tool — is lower-value than it looked before the terminal UX and permission-model work landed; Phase 4's sandboxing is probably next in line, once an auto-approve mode makes it non-optional.
- **Don't front-load:** a second provider, sandboxing, and IDE integration. All three are real work that only pays off once chisel is something you're actually depending on daily.

---

### Sources consulted

1. [Dive into Claude Code](https://github.com/VILA-Lab/Dive-into-Claude-Code) — source-level architecture analysis of Claude Code v2.1.88.
2. [OpenAI Cookbook — "Build a coding agent with GPT-5.1"](https://developers.openai.com/cookbook/examples/build_a_coding_agent_with_gpt-5.1)
3. [Aider documentation — provider support via LiteLLM](https://aider.chat/docs/llms/other.html)
4. [OpenCode documentation — provider list and authentication flows](https://opencode.ai/docs/providers/)
5. OpenCode Go's live `/v1/models` and direct request/response testing against `https://opencode.ai/zen/go` (2026-07) — the primary source for §3–5 at this point; supersedes earlier Anthropic-pricing-based cost analysis, which no longer applies now that chisel doesn't call Anthropic's API.

*Note: earlier research on the general CLI-agent landscape (§1) came from an automated pass that hit a session rate limit partway through adversarial verification, so a few claims there are single-source rather than cross-checked. Nothing found contradicted them.*
