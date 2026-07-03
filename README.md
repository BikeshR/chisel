# chisel

A personal terminal coding agent: a Bubbletea TUI wrapped around an
[OpenCode Go](https://opencode.ai/go) model with a small, fixed tool set
(`bash`, file editing, `glob`, `grep`).

## Build & run

```sh
go build -o chisel .
./chisel
```

Requires `CHISEL_API_KEY` (your OpenCode Go key) in the environment, or set
it in `~/.chisel.env` (outside the repo, never committed) along with
`CHISEL_MODEL` to pick a model. Chisel operates on the directory it's run
from. Switch models anytime in-app with `/model`.

## Status

Phase 1 (minimum viable agent) done, Phase 2 (daily driver) in progress —
see [`docs/design.md`](docs/design.md) for the full design notes,
architecture rationale, and roadmap.
