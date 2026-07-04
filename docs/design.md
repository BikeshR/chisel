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

**MCP** remains the highest-leverage item still on the roadmap (Phase 3,
§5) — a standard that unlocks an entire ecosystem of tools chisel doesn't
have to build itself, independent of which wire format the model API uses.

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

### Phase 2 — Daily driver — **mostly done**

- ~~Streamed output instead of waiting for the full response~~ — **done**: hand-rolled SSE decode, text deltas render live via a channel + `tea.Cmd` re-arm loop (`internal/tui/stream.go`)
- ~~A config file for model choice~~ — **done**: `CHISEL_MODEL` env var (switchable at runtime with `/model`), plus an optional `~/.chisel.env` (outside the repo, never committed) for `CHISEL_API_KEY`/`CHISEL_BASE_URL`/`CHISEL_MODEL`. Working-directory scope and an allowed-commands list are still open.
- ~~Model health-check~~ — **done**: `/model check [id]` (`internal/tui/commands.go`) sends a minimal request through chisel's real request shape (system prompt + tools included) and reports pass/fail with the actual reply or error. Caught a real, useful finding immediately: `deepseek-v4-flash` and `kimi-k2.6`, both recorded as failing in §4 earlier in July 2026, now work — confirms this needed to be a live check, not a maintained static list.
- ~~Session persistence~~ — **done**: `internal/session` saves the conversation (`[]agent.Message`) to `~/.chisel/sessions/<sanitized-workdir>.json` after every turn, scoped per working directory so separate projects don't share history. `main.go` loads it back on startup and `tui.New` reconstructs the visible transcript from it (`internal/tui/history.go`), skipping the permission-prompt step since every replayed tool call was already resolved in a past run. Deliberately doesn't restore which model was selected — that's a per-run choice (`CHISEL_MODEL`/default), not part of "the conversation". `/new` abandons the current transcript, in memory and on disk.
- Git awareness: show a diff before writing, optionally auto-commit like Aider
- ~~Persistent bash session~~ — **done**: `agent.BashSession` (`internal/agent/bashsession.go`) runs one long-lived `sh` process for the life of the TUI session (owned by `main.go`, independent of `/model` switches) instead of a fresh subprocess per call, so `cd` and exported env vars now carry across calls. Completion is detected via a random sentinel line echoed after each command, carrying its exit code; a 2-minute per-command timeout kills and drops the session (recreated lazily on the next call) rather than risking a desynced read. `{"restart": true}` (already part of the tool's schema) now does something real: kills the shell and starts a fresh one rooted back at workDir.

**How chisel got here — history worth keeping:**

- Chisel briefly supported *two* providers at once (real Anthropic + OpenCode Go, routed by a model-string prefix), before the decision in §3 to drop Anthropic entirely and go OpenCode-Go-only. That dual-provider code, and the SDK it depended on, no longer exists.
- **While still on the Anthropic-compatible endpoint**, direct bisection testing found that Anthropic's built-in `bash`/`text_editor` tools (schema-less by design — Anthropic handles them specially server-side) don't work through OpenCode's translation layer at all: a bare request with no tools succeeded on working models, but adding either built-in tool made those same models fail with a generic upstream 400. A custom tool with a real JSON schema worked fine. This is *why* chisel writes its own schemas for every tool now rather than leaning on any provider's built-ins — it's not just an OpenAI-format requirement, it was already true against OpenCode's Anthropic-compatible endpoint too.
- **Also found on that endpoint:** a real bug, independent of OpenCode — the Anthropic SDK's SSE decoder didn't recognize a plain `application/json` error response (as opposed to a proper `text/event-stream` error event) as a failure at all; it silently reported zero events and a `nil` error, which chisel was treating as an empty success. The rewrite in §3 fixes this more robustly by checking the HTTP status code directly before ever starting to decode the stream — a non-200 response is now always a clean, immediate error.
- **Base URL path gotcha** (kept in case it recurs): whichever client library or hand-rolled request builder you're using, don't include `/v1` in a configured base URL if the client appends `/v1/...` itself — produces a silently-wrong double path and a 404 rather than an obvious error.

### Phase 3 — Real capability

- MCP client support — the highest-leverage single addition (see §3)
- Context management: manual context editing/summarization as the window fills (no server-side compaction to lean on now — that was an Anthropic API feature)
- Plan mode — a read-only planning turn, presented for confirmation before execution begins
- Subagents — spawn a child instance of the same loop with a narrower tool set for a delegated subtask
- Hooks — pre/post-tool-call callbacks (run a linter after every edit, block writes to certain paths)
- TUI polish: a dedicated diff view, split panes, Lipgloss theming

### Phase 4 — Parity-and-beyond

- Sandboxing the bash tool (container or OS-level sandbox) — non-negotiable once there's an auto-approve/"just do it" mode
- A second provider, if one ever genuinely earns its way in — would mean reintroducing a thin `Provider` interface, deliberately not built speculatively this time either
- User-defined skill files chisel loads on demand
- Background/async task execution with completion notifications
- Lightweight IDE integration, if it turns out to matter for daily use

## 6. Bottom line

- **Provider:** OpenCode Go, exclusively. One API key (`CHISEL_API_KEY`), OpenAI-compatible `/v1/chat/completions`, no SDK dependency.
- **Stack:** Go, hand-rolled HTTP/SSE client, Bubbletea + Lipgloss + Bubbles for the TUI — built from day one, not deferred.
- **Model:** `minimax-m3` as the default (confirmed working); switch with `/model`.
- **Highest-leverage next investment:** session persistence + git awareness (finish Phase 2), then an MCP client (Phase 3).
- **Don't front-load:** a second provider, sandboxing, and IDE integration. All three are real work that only pays off once chisel is something you're actually depending on daily.

---

### Sources consulted

1. [Dive into Claude Code](https://github.com/VILA-Lab/Dive-into-Claude-Code) — source-level architecture analysis of Claude Code v2.1.88.
2. [OpenAI Cookbook — "Build a coding agent with GPT-5.1"](https://developers.openai.com/cookbook/examples/build_a_coding_agent_with_gpt-5.1)
3. [Aider documentation — provider support via LiteLLM](https://aider.chat/docs/llms/other.html)
4. [OpenCode documentation — provider list and authentication flows](https://opencode.ai/docs/providers/)
5. OpenCode Go's live `/v1/models` and direct request/response testing against `https://opencode.ai/zen/go` (2026-07) — the primary source for §3–5 at this point; supersedes earlier Anthropic-pricing-based cost analysis, which no longer applies now that chisel doesn't call Anthropic's API.

*Note: earlier research on the general CLI-agent landscape (§1) came from an automated pass that hit a session rate limit partway through adversarial verification, so a few claims there are single-source rather than cross-checked. Nothing found contradicted them.*
