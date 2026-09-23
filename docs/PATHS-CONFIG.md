---
title: Paths and config — Linux-style everywhere
status: authoritative
date: 2026-09-22
supersedes: [compatible-implementation-spec.md §6]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# PATHS-CONFIG — Linux standard, Windows-compatible

Single resolver. No `os.UserConfigDir` branches. No `~/Library/*`, no `%AppData%`, no `~/.tk` writes on fresh installs.

```text
~/.config/true-knowledge/       config.json (tk source of truth)
~/.local/share/true-knowledge/  tk.json (name→path), graph-refs.json
~/.cache/true-knowledge/        == CBM_CACHE_DIR (_config.db, indexes)
~/.local/state/true-knowledge/  logs/tk.log, history.jsonl, rendezvous/ (CBM_RUNTIME_DIR)
```

Resolution: `$XDG_{CONFIG,DATA,CACHE,STATE}_HOME/true-knowledge/` or `$HOME/{.config,.local/share,.cache,.local/state}/true-knowledge/`. On Windows `$HOME` = `%USERPROFILE%` via `filepath.Join` (e.g. `C:\Users\you\.config\true-knowledge\`).

Overrides (tests + isolation): `TK_CONFIG_HOME / TK_DATA_HOME / TK_CACHE_HOME / TK_STATE_HOME`, or single `TK_HOME=/tmp/x → {config,data,cache,state}`. Every test uses an isolated home.

Spawn env (every `cbm` call): `CBM_CACHE_DIR=<cache>/`, `CBM_RUNTIME_DIR=<state>/rendezvous`, `CBM_ALLOWED_ROOT` (from tk config).

Migration: `tk migrate [--from ~/.tk|$TK_HOME|~/Library/Application Support/true-knowledge] --dry-run` copies old flat layout once, writes `MIGRATED` marker, refuses re-run without `--force`.
