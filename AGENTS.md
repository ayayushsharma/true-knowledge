# AGENTS.md — true-knowledge / tk

> Thin Go shipper over `codebase-memory-mcp` (CBM). Linux-first, Windows-compatible.
> `tk` owns paths + config + spawn + render. CBM owns graph, store, daemon, watcher.
> Docs authority: `docs/00-AUTHORITY.md` > newest `docs/DECISIONS/*` > `docs/*.md` > `spec-v1` (frozen history).

## What this repo is

* Single static Go binary `tk`: `setup/init/register/index/sync/status/find/explain/grep/source-search/outline/impact/arch/query/validate/cbm/daemon/mcp/config/migrate/install/mcp-install/completion`. `kg_find/kg_explain/kg_grep` are aliases of `find/explain/grep`. `daemon` is CLI-only (never MCP).
* No own indexer/parser/SQLite touch. Graph work = `codebase-memory-mcp cli <tool> --args-file` (raw-JSON argv is deprecated upstream) via one wrapper (`internal/cbmexec`); text work = explicit `source-search` via Zoekt linked as a Go library (`internal/zoekttext`, no magic routing, no subprocess).
* tk installs ALL its external dependencies itself (`internal/backends` registry + `internal/installer`: pinned, checksum-verified, `<cache>/bin`; CBM today). Zoekt is a `go.mod` pin, not a backend — same-language links, cross-language spawns. No apt/brew/npm/toolchain at runtime. `tk setup` = init + install + opt-in register/client.
* No `tk` supervisor daemon. CBM coordination daemon is shared per-account (first-starts/last-stops). `cli` mode is daemon-free one-shot.

## Paths (Linux-style everywhere, incl. macOS)

```text
~/.config/true-knowledge/       config.json (tk source of truth)
~/.local/share/true-knowledge/  tk.json (name→path, heads, fingerprints)
~/.cache/true-knowledge/        == CBM_CACHE_DIR (_config.db, indexes) + zoekt/ shards
~/.local/state/true-knowledge/  logs/tk.log (unified JSONL trace), rendezvous/ (CBM_RUNTIME_DIR)
```

* Never write `~/.tk`, `~/Library/*`, `%AppData%`. Always `true-knowledge/` subfolder.
* Overrides for tests: `TK_CONFIG_HOME/TK_DATA_HOME/TK_CACHE_HOME/TK_STATE_HOME`, or single `TK_HOME=/tmp/x → {config,data,cache,state}`. Every test uses isolated home.
* Propagate on every spawn: `CBM_CACHE_DIR`, `CBM_RUNTIME_DIR=<state>/rendezvous`, `CBM_ALLOWED_ROOT` (from tk config).

## Commands (run from repo root)

```bash
go build -o tk ./cmd/tk
TK_HOME=/tmp/tk-test ./tk init && TK_HOME=/tmp/tk-test ./tk register ./fixture --name demo
TK_HOME=/tmp/tk-test ./tk index demo
TK_HOME=/tmp/tk-test ./tk sync demo        # clean HEAD = no-op
TK_HOME=/tmp/tk-test ./tk status --json
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | TK_HOME=/tmp/tk-test ./tk mcp
./tk completion bash|zsh|fish|powershell
gofmt -l . && go vet ./... && go test ./...
```

## Trace log (`<state>/logs/tk.log`, JSONL — read with Unix tools, never a tk command)

```bash
tail -n 50 tk.log | jq .                                  # recent calls
jq -c 'select(.exit != 0)' tk.log                         # failures only
jq -r '[.ts, (.argv|join(" "))] | @tsv' tk.log            # argv history
jq -r 'select(.mcp.tool=="source_search") | .output.text' tk.log
```

## Conventions for agents (human or AI)

1. **Delegate, don't reimplement:** parsing, FTS, embeddings, watcher, `install` matrix (45 clients), UI `:9749`, `.zst` artifacts = CBM. If you touch `*.db` directly or write client `mcp.json` by hand, it's a bug — call `cbm cli` / `cbm install --dry-run`.
2. **Indexing:** default `moderate`; `fast` for watcher/auto, `full` explicit only; `cross-repo-intelligence` only after fresh bases + `target_projects=["*"]`. Check `check_index_coverage` before negative claims. Respect `.cbmignore` order + `512MiB` cap + `index_max_*` (fail-whole-preserve-serving).
3. **Freshness:** `tk sync` = `git HEAD` + coverage → no-op or let watcher do it. Never force full on save.
4. **Fail-open + budgets:** CBM down → clear `tk install` hint, never block agent. Truncate by whole records + `...truncated`.
5. **Completion:** Cobra only. Dynamic: projects (from `tk.json` + `list_projects`), config keys, `--client pi,opencode,claude,codex`. Test `tk __complete`.
6. **Close fast:** no flush/stop on session end. stdin EOF = instant exit. Only `install --update` holds admission barrier to deadline.
7. **Docs:** change = new `docs/DECISIONS/YYYY-MM-DD-<slug>.md` + bump affected `docs/*.md` header (`status/date/supersedes`). Never edit history except `superseded-by` stamp. `grep -r "status: authoritative" docs/` is truth.

## Key references

* CBM: `README.md #session-coordination-daemon #cli-mode #auto-index`, `docs/CONFIGURATION.md §2/§4`, `docs/INDEX_RESOURCE_LIMITS.md`, `docs/cbmignore.md`, `server.json`.
* This repo: `compatible-implementation-spec.md` (v1 frozen), `docs/00-AUTHORITY.md`, `docs/INDEXING.md`, `docs/CBM-BOUNDARY.md`, `docs/PATHS-CONFIG.md`, `docs/AGENT-PROFILES.md`, `docs/ROADMAP.md`.

## PR checklist

* [ ] Isolated `TK_HOME`, no `~/.tk`/`Library` writes
* [ ] Single spawn wrapper used, env map set
* [ ] Coverage checked, budgets honored, fail-open
* [ ] Completion + `--json` + `--help` updated
* [ ] No secrets in `tk.log` (redaction patterns cover any new secret-shaped output)
* [ ] Docs header + ADR added if behavior changed
