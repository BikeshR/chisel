# chisel

A personal terminal coding agent: a Bubbletea TUI wrapped around an
[OpenCode Go](https://opencode.ai/go) model with a small, fixed tool set
(`bash`, file editing, `glob`, `grep`), extensible with MCP servers.

## Build & run

```sh
go build -o chisel .
./chisel
```

Requires `CHISEL_API_KEY` (your OpenCode Go key) in the environment, or set
it in `~/.chisel.env` (outside the repo, never committed) along with
`CHISEL_MODEL` to pick a model. Chisel operates on the directory it's run
from. Switch models anytime in-app with `/model`, or live-test one with
`/model check [name]`.

Conversations save automatically per working directory
(`~/.chisel/sessions/`) and resume the next time you run chisel there;
`/new` starts a fresh one.

File edits show a diff before you approve them. `/git auto on` (off by
default) makes chisel commit its own changes after each turn, Aider-style.

The status bar shows the current request's context size separately from
cumulative token spend; past 100k tokens it'll suggest `/compact`, which
asks the model to summarize the conversation so far and replaces the
history with that summary.

`/plan` toggles plan mode: the model is told to only explore and present
a plan rather than make changes, and any mutating action it tries anyway
is refused outright — not just discouraged by the prompt. `/plan` again
to exit and allow changes.

## MCP servers

List servers in `~/.chisel/mcp.json` — same `mcpServers` shape Claude
Desktop and Claude Code use:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "..." }
    }
  }
}
```

Discovered tools show up as `server: tool` in the permission prompt and
always require approval — chisel has no way to know what an arbitrary
server's tool actually does. A server that fails to start is logged to
stderr and skipped; chisel still runs fine with whatever did start.

## Development

```sh
make check    # fmt + vet + lint + unit tests — no network needed
make build    # ./chisel
make install  # go install . — puts chisel on your PATH
```

`golangci-lint` needs installing separately (see
[golangci-lint.run](https://golangci-lint.run/welcome/install/)) — CI runs
it on every push. Live-network tests against the real API
(`make integration-test`) need `CHISEL_API_KEY` set and aren't part of CI.

## Status

Phase 1 (minimum viable agent) and Phase 2 (daily driver) done, Phase 3
(real capability) in progress — see [`docs/design.md`](docs/design.md)
for the full design notes, architecture rationale, and roadmap.
