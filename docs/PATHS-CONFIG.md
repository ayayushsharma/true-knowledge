---
title: Paths and config — Linux-style everywhere
status: authoritative
date: 2026-09-23
supersedes: [compatible-implementation-spec.md §6, docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (memory paths)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

## Config keys (dotted, `tk config set/list`)

`index_mode`, `auto_index`, `auto_watch`, `watcher_enabled`, `allowed_root`, `cbm_binary`, `cbm_version_pin`, `budgets.default_chars|architecture_chars|notes_toc_chars|ledger_chars`, `embedding.enabled|endpoint|model|timeout_ms`, `ledger.enabled`. Unknown keys error; `set` validates at write time (enabling embeddings requires an endpoint + model).

# PATHS-CONFIG — Linux standard, Windows-compatible

Single resolver. No `os.UserConfigDir` branches. No `~/Library/*`, no `%AppData%`, no `~/.tk` writes on fresh installs.

```text
~/.config/true-knowledge/       config.json (tk source of truth)
~/.local/share/true-knowledge/  tk.json (name→path, heads, fingerprints), mem/ (facts.db), notes/ (<project>/*.md source-of-truth + index.db), ledger/ (<project>.json)
~/.cache/true-knowledge/        == CBM_CACHE_DIR (_config.db, indexes) + zoekt/ shards
~/.local/state/true-knowledge/  logs/tk.log (unified JSONL trace), rendezvous/ (CBM_RUNTIME_DIR)
```

tk-owned memory trees (all 0600 files, dirs 0700, CBM never touches them):

```text
<data>/mem/facts.db        facts + review_queue (SQLite, WAL, user_version)
<data>/notes/index.db      derived FTS5 BM25 index (rebuildable via `tk note reindex`)
<data>/notes/<project>/    approved notes as front-matter markdown (the durable truth)
<data>/notes/review/       <project>.jsonl captured notes awaiting approval
<data>/ledger/<project>.json  bounded per-project working terms (full-text replace)
```

Resolution: `$XDG_{CONFIG,DATA,CACHE,STATE}_HOME/true-knowledge/` or `$HOME/{.config,.local/share,.cache,.local/state}/true-knowledge/`. On Windows `$HOME` = `%USERPROFILE%` via `filepath.Join` (e.g. `C:\Users\you\.config\true-knowledge\`).

Overrides (tests + isolation): `TK_CONFIG_HOME / TK_DATA_HOME / TK_CACHE_HOME / TK_STATE_HOME`, or single `TK_HOME=/tmp/x → {config,data,cache,state}`. Every test uses an isolated home.

Spawn env (every `cbm` call): `CBM_CACHE_DIR=<cache>/`, `CBM_RUNTIME_DIR=<state>/rendezvous`, `CBM_ALLOWED_ROOT` (from tk config).

Migration: `tk migrate [--from ~/.tk|$TK_HOME|~/Library/Application Support/true-knowledge] --dry-run` copies old flat layout once, writes `MIGRATED` marker, refuses re-run without `--force`.
