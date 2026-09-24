---
title: Paths and config — Linux-style everywhere
status: authoritative
date: 2026-09-25
supersedes: [compatible-implementation-spec.md §6, docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (memory paths), docs/DECISIONS/2026-09-24-dynamic-mcp-profile-env.md (MCP profile env), docs/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md (ui.picker config key), docs/DECISIONS/2026-09-25-ledger-append-only-history-prune.md (ledger storage + ledger_chars semantics)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

## Config keys (dotted, `tk config set/list`)

`index_mode`, `auto_index`, `auto_watch`, `watcher_enabled`, `allowed_root`, `cbm_binary`, `cbm_version_pin`, `budgets.default_chars|architecture_chars|notes_toc_chars|ledger_chars` (`ledger_chars` = retrieval-only cap on `ledger get`, never applied on write), `embedding.enabled|endpoint|model|timeout_ms`, `ledger.enabled`, `mcp.profile`, `ui.picker` (fuzzy project picker on multi-project + TTY, default true; see human-UX ADR). Unknown keys error; `set` validates at write time (enabling embeddings requires an endpoint + model; `mcp.profile` must be one of `scout|analysis|minimal|memory` or empty).

## Runtime env vars

`TK_CONFIG_HOME/TK_DATA_HOME/TK_CACHE_HOME/TK_STATE_HOME` / `TK_HOME` (see above) and `TK_MCP_PROFILE` — the MCP tool surface for `tk mcp`, resolved as explicit `--tool-profile` flag > `TK_MCP_PROFILE` > config `mcp.profile` > `scout`. Invalid values fail loudly, never fall back silently. Clients set it per model/project via their own MCP server `env` (see docs/AGENT-PROFILES.md).

# PATHS-CONFIG — Linux standard, Windows-compatible

Single resolver. No `os.UserConfigDir` branches. No `~/Library/*`, no `%AppData%`, no `~/.tk` writes on fresh installs.

```text
~/.config/true-knowledge/       config.json (tk source of truth)
~/.local/share/true-knowledge/  tk.json (name→path, heads, fingerprints), mem/ (facts.db), notes/ (<project>/*.md source-of-truth + index.db), ledger/ (<project>.jsonl append-only)
~/.cache/true-knowledge/        == CBM_CACHE_DIR (_config.db, indexes) + zoekt/ shards
~/.local/state/true-knowledge/  logs/tk.log (unified JSONL trace), rendezvous/ (CBM_RUNTIME_DIR)
```

tk-owned memory trees (all 0600 files, dirs 0700, CBM never touches them):

```text
<data>/mem/facts.db        facts + review_queue (SQLite, WAL, user_version)
<data>/notes/index.db      derived FTS5 BM25 index (rebuildable via `tk note reindex`)
<data>/notes/<project>/    approved notes as front-matter markdown (the durable truth)
<data>/notes/review/       <project>.jsonl captured notes awaiting approval
<data>/ledger/<project>.jsonl  append-only log (5 keys, last-write-wins get fold, retrieval-only budget, human-only prune; see ledger ADR)
```

Resolution: `$XDG_{CONFIG,DATA,CACHE,STATE}_HOME/true-knowledge/` or `$HOME/{.config,.local/share,.cache,.local/state}/true-knowledge/`. On Windows `$HOME` = `%USERPROFILE%` via `filepath.Join` (e.g. `C:\Users\you\.config\true-knowledge\`).

Overrides (tests + isolation): `TK_CONFIG_HOME / TK_DATA_HOME / TK_CACHE_HOME / TK_STATE_HOME`, or single `TK_HOME=/tmp/x → {config,data,cache,state}`. Every test uses an isolated home.

Spawn env (every `cbm` call): `CBM_CACHE_DIR=<cache>/`, `CBM_RUNTIME_DIR=<state>/rendezvous`, `CBM_ALLOWED_ROOT` (from tk config).

Migration: `tk migrate [--from ~/.tk|$TK_HOME|~/Library/Application Support/true-knowledge] --dry-run` copies old flat layout once, writes `MIGRATED` marker, refuses re-run without `--force`.
