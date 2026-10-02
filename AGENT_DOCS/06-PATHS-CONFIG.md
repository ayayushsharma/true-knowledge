---
id: 06-paths-config
title: Paths and config — XDG everywhere, one key registry
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-10-02-stderr-is-the-diagnostic-channel.md, AGENT_DOCS/history/DECISIONS/2026-10-02-picker-search-is-fuzzy-and-case-insensitive.md, AGENT_DOCS/history/DECISIONS/2026-10-02-project-routing-is-always-explicit.md, AGENT_DOCS/history/compatible-implementation-spec.md §6, AGENT_DOCS/history/PATHS-CONFIG.md, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp3-memory-layer.md, AGENT_DOCS/history/DECISIONS/2026-09-24-dynamic-mcp-profile-env.md, AGENT_DOCS/history/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md, AGENT_DOCS/history/DECISIONS/2026-09-25-comments-pass-fixes.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

# 06-PATHS-CONFIG — one resolver, no platform branches

Linux-style paths everywhere, including macOS. Never `~/.tk`, never
`~/Library/*`, never `%AppData%`. Always a `true-knowledge/` subfolder. One
resolver, no `os.UserConfigDir` branching.

```text
~/.config/true-knowledge/       config.json, <config>/ignore
~/.local/share/true-knowledge/  tk.json, mem/, notes/, ledger/
~/.cache/true-knowledge/        CBM_CACHE_DIR (_config.db, indexes) + zoekt/
~/.local/state/true-knowledge/  logs/tk.log, rendezvous/
```

Resolution: `$XDG_{CONFIG,DATA,CACHE,STATE}_HOME/true-knowledge/`, else
`$HOME/{.config,.local/share,.cache,.local/state}/true-knowledge/`. On Windows
`$HOME` is `%USERPROFILE%` joined with `filepath.Join`, so the layout is
`C:\Users\you\.config\true-knowledge\`.

Overrides, for tests and isolation: `TK_CONFIG_HOME`, `TK_DATA_HOME`,
`TK_CACHE_HOME`, `TK_STATE_HOME`, or one `TK_HOME=/tmp/x` that expands to all
four. Every test uses an isolated home, and the `--home` flag is the same thing
as `TK_HOME`.

## What lives where

| Path | Contents |
|---|---|
| `<config>/config.json` | the tk source of truth; `version` stays `1` |
| `<config>/ignore` | per-line plain-directory text-index filters, gitignore-lite; see `05-INDEXING.md` |
| `<data>/tk.json` | project name → path, `head`, `zoekt_head`, fingerprints |
| `<data>/mem/facts.db` | facts plus review queue, SQLite, WAL, `user_version` |
| `<data>/notes/index.db` | derived FTS5 BM25 index, rebuildable with `tk note reindex` |
| `<data>/notes/<project>/` | approved notes as front-matter markdown, the durable truth |
| `<data>/notes/review/` | `<project>.jsonl`, captured notes awaiting approval |
| `<data>/ledger/<project>.jsonl` | append-only ledger |
| `<cache>/zoekt/<project>/` | text-index shards |
| `<cache>/bin/` | managed backend binaries; tk reads the installed version back from the binary, so there is no installed-version ledger file |
| `<state>/logs/tk.log` | unified JSONL trace, `0600`, 10 MiB ×2 rotation |
| `<state>/rendezvous/` | `CBM_RUNTIME_DIR` |

Memory files are `0600` and their directories are `0700`. CBM never touches the
memory tree.

## Spawn environment

Every CBM call carries:

```text
CBM_CACHE_DIR=<cache>/
CBM_RUNTIME_DIR=<state>/rendezvous
CBM_ALLOWED_ROOT=<from config allowed_root>
```

On Linux and macOS the spawn wrapper additionally enforces an 8MB
`RLIMIT_STACK` floor for the engine (raise via `ulimit -s … && exec` when the
inherited soft limit is below it), because the OS sizes the C engine's main
thread from the *parent's* inherited limit — see
`AGENT_DOCS/history/DECISIONS/2026-09-28-cbm-stack-floor-at-spawn.md`.

## Config keys

Dotted keys, managed by one registry inside `internal/config` — the package
that owns the shape also owns the key list. Unknown keys are an error.
`tk config set` validates at write time; `tk config get|list|reset|validate`
round out the surface.

| Key | Default | Notes |
|---|---|---|
| `index_mode` | `moderate` | `fast` for watcher and auto paths, `full` explicit only |
| `auto_index` | `true` | forwarded to CBM |
| `auto_watch` | `true` | forwarded to CBM |
| `watcher_enabled` | `true` | read once at daemon start, so a flip needs `tk daemon stop` |
| `allowed_root` | — | becomes `CBM_ALLOWED_ROOT` |
| `cbm_binary` | — | explicit path, second in resolver order |
| `cbm_version_pin` | `0.11.0` | `X.Y.Z`, or 7–40 hex chars; validated at `set` time |
| `budgets.default_chars` | `6000` | character bound, applied as whole-row drops |
| `budgets.architecture_chars` | `2200` | |
| `budgets.notes_toc_chars` | `700` | title-only listing, never bodies |
| `budgets.ledger_chars` | `1500` | retrieval-only cap on `ledger get`; never applied on write |
| `embedding.enabled` | `false` | |
| `embedding.endpoint` | `http://127.0.0.1:11434` | Ollama-compatible; required when enabled |
| `embedding.model` | — | required when enabled |
| `embedding.timeout_ms` | `3000` | |
| `ledger.enabled` | `true` | write-only gate; reads stay open |
| `mcp.profile` | — | `scout`, `analysis`, `minimal`, `memory`, or empty |
| `ui.picker` | `true` | gates `--select`, the only flag that opens the fuzzy picker; off, or without a TTY or without `--json`, `--select` fails naming `--project` |

Shape rules:

* Nested JSON objects, not dotted strings: `budgets{}`, `embedding{}`,
  `ledger{}`, `mcp{}`, `ui{}`. The legacy flat `mcp_profile` key still loads and
  migrates to nested on the next save, and both shapes coexist on read.
* Unknown fields are rejected. Silent schema drift is worse than a loud
  one-time break in a pre-release tool.
* Defaults inheritance: decoding seeds the full default set first, so absent keys
  inherit and a present group replaces wholesale. `{"mcp":{}}` therefore means
  "MCP config is default", not "MCP config is zeroed".
* Enabling embeddings without both an endpoint and a model fails at write time,
  and an invalid `mcp.profile` fails at write time too.

## Environment variables

| Variable | Effect |
|---|---|
| `TK_CONFIG_HOME` / `TK_DATA_HOME` / `TK_CACHE_HOME` / `TK_STATE_HOME` | per-root override |
| `TK_HOME` | all four at once |
| `TK_MCP_PROFILE` | MCP tool surface; loses to `--tool-profile`, beats config `mcp.profile` |
| `TK_CBM_BIN` | wins the backend resolver |
| `TK_RELEASE_BASE_URL` | release-asset mirror base; also how the install tests serve a fake release |
| `TK_RELEASE_BASE_URL_CBM` | per-backend mirror, wins over the global one |
| `TK_LOG` | stderr diagnostic level `off\|error\|warn\|info\|debug`, default `warn`; `--verbose`/`--quiet` override it — see `10-OUTPUT.md` |

## Migration

```bash
tk migrate --dry-run
tk migrate --from ~/.tk
```

Detects `~/.tk`, `$TK_HOME`, and `~/Library/Application Support/true-knowledge`.
Copies the old flat layout once, writes a `MIGRATED` marker, and refuses a
re-run without `--force`.
