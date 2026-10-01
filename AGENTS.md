# AGENTS.md — true-knowledge / tk

> Truth lives in `AGENT_DOCS/`, read in numeric order. This file is the
> auto-loaded entry point: a pointer plus the rules that must never be missed.
> Precedence law, doc-style contract, and the doc-change procedure:
> `AGENT_DOCS/00-INDEX.md`.

`tk` is a thin Go shipper over `codebase-memory-mcp` (CBM). Linux and macOS
first; Windows a follow-up. tk owns paths, config, one spawn, and render; CBM
owns the graph, the store, the daemon, and the watcher. `tk` installs every
external dependency itself — no apt, brew, npm, or toolchain at runtime. There is no `tk`
supervisor daemon; the CBM coordination daemon is shared per account. The
resident (`tk mcp --detach`) is **built**: one warm CBM child behind an owner-only
socket, opt-in, no idle timeout, reads dial it first and fall back to a spawn.
That line above still holds — it watches nothing, restarts nothing, and dies on
SIGTERM — so it needs no change. It owns exactly one socket and one pid file, both
under `TK_HOME`, both naming its own process; see
`AGENT_DOCS/history/DECISIONS/2026-09-30-resident-owns-its-endpoint-and-its-swap.md`.

## Read this first

| Question | File |
|---|---|
| What is tk, and what does it own | `AGENT_DOCS/01-ARCHITECTURE.md` |
| What tk must never do | `AGENT_DOCS/02-BOUNDARY.md` |
| Exact command and flag surface | `AGENT_DOCS/03-COMMANDS.md` |
| MCP profiles, tools, result shape | `AGENT_DOCS/04-MCP.md` |
| Index modes, discovery, freshness | `AGENT_DOCS/05-INDEXING.md` |
| Paths, config keys, env vars | `AGENT_DOCS/06-PATHS-CONFIG.md` |
| Facts, notes, ledger, secrets | `AGENT_DOCS/07-MEMORY.md` |
| What is not built, and why | `AGENT_DOCS/08-BACKLOG.md` |
| Operating rules and the PR gate | `AGENT_DOCS/09-CHECKLIST.md` |
| Why a decision was made | `AGENT_DOCS/history/DECISIONS/` |

## Rules that must never be missed

1. **Delegate, never reimplement.** If you touch a `*.db` directly or hand-write
   a client `mcp.json`, it is a bug — call `cbm cli`, or `cbm install --dry-run`.
2. **One spawn wrapper.** Everything that runs CBM goes through
   `internal/cbmexec` with the environment map set. Raw-JSON argv is deprecated
   upstream; use `--args-file`.
3. **Isolated homes.** Every test uses `TK_HOME` or the `TK_*` overrides. Never
   write `~/.tk`, `~/Library/*`, or `%AppData%` — always a `true-knowledge/`
   subfolder.
4. **Coverage before absence.** Run `check_index_coverage` before any negative
   claim about a codebase. A failed probe is a hard error, never a silent
   absence. `search_graph`, `search_code`, and `trace_path` already annotate
   their empty results; read the verdict.
5. **Fail open, respect budgets.** CBM down means a `tk install` hint, never a
   blocked agent. Truncate by whole records and mark it. Never character-slice
   JSON.
6. **Completion is Cobra's.** Dynamic projects, config keys, `--client`,
   `--tool-profile`. Test with `tk __complete`.
7. **Close fast.** stdin EOF is an instant exit. No flush, no stop, no shutdown
   deadline. Only `install --update` holds an admission barrier.
8. **Docs change the way the index does.** New dated ADR in
   `AGENT_DOCS/history/DECISIONS/`, then the rule in the affected `NN-*.md` with
   a bumped header. History is immutable and `go test ./internal/docs` enforces
   it. A file over 200 lines is a bug.
9. **Scan throwaway docs before any long search.** Read
   `AGENT_DOCS/THROWAWAY/INDEX.md` before grepping the repo or the web for
   background. Not instead of verifying — instead of rediscovering. Record a
   finding there while you have the context; promote it to an ADR when it
   becomes a decision and delete it here. Notes there are never authoritative.
10. **Verify load-bearing claims; never generalize from a scoped note.** A
    documented exception about one tool is not a property of the system —
    `README.md:697` names `manage_adr`, and reading it as "long processes serve
    stale data" produced an entire discarded subsystem. "No mechanism exists" is
    not "no behaviour exists." Before designing around a path, check which file
    the queries actually open; before trusting a lifetime or cache claim, probe a
    real engine and record the environment; re-probe after a CBM upgrade. The
    three wrong assumptions this rule exists for:
    `AGENT_DOCS/THROWAWAY/2026-09-30-warm-child-store-freshness.md`.

## Commands

```bash
mise run build       # or: go build -o tk ./cmd/tk
mise run docs-man    # regenerate docs/man/tk*.1
mise run e2e         # fake-engine CLI + MCP matrix, isolated TK_HOME
mise run e2e-real REPO=/path/to/repo   # real CBM, real repo, ~3 min
gofmt -l . && go vet ./... && go test ./...
```

```bash
TK_HOME=/tmp/tk-test ./tk init && TK_HOME=/tmp/tk-test ./tk register ./fixture --name demo
TK_HOME=/tmp/tk-test ./tk index demo
TK_HOME=/tmp/tk-test ./tk sync demo        # clean HEAD = no-op
TK_HOME=/tmp/tk-test ./tk status --json
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | TK_HOME=/tmp/tk-test ./tk mcp
```

The trace log is `<state>/logs/tk.log`, JSONL. Read it with Unix tools; there is
no `tk log` command.

```bash
tail -n 50 tk.log | jq .                                  # recent calls
jq -c 'select(.exit != 0)' tk.log                         # failures only
jq -r '[.ts, (.argv|join(" "))] | @tsv' tk.log            # argv history
jq -r 'select(.mcp.tool=="source_search") | .output.text' tk.log
```

## Pre-release law

`tk` is unreleased. `--json` envelopes, MCP result shapes, flag names, and tool
lists carry no compatibility guarantee: keys may change in any release,
including a patch, and there is no schema version. A caller who needs a shape to
hold still pins a commit. What *is* protected is the human CLI surface and the
boundary rules. Full statement: `AGENT_DOCS/00-INDEX.md`.

## Conventions for agents

* **Indexing:** default `moderate`; `fast` for the watcher and auto paths;
  `full` only when explicitly asked for; `cross-repo-intelligence` only after
  fresh bases, `target_projects=["*"]`, and once per source project — N sources
  link an N-repository clique.
* **MCP profiles:** `scout` (11), `analysis` (14), `minimal` (3), `memory` (22),
  chosen at runtime by `--tool-profile` > `TK_MCP_PROFILE` > config
  `mcp.profile` > `scout`. `daemon` is CLI-only, never MCP.
* **Memory:** `mem`/`note`/`ledger` are tk-owned and CBM-free. The ledger is
  append-only with five keys and a human-only prune.
* **Unix rule:** if `tail`, `grep`, or `jq` already does the job, tk does not
  ship a command for it.
