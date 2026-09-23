---
title: Thin tk over CBM (supplants v1 daemon)
status: authoritative
date: 2026-09-22
supersedes: [compatible-implementation-spec.md §5.2, §6, §12, §13, §15]
superseded-by: null
---

# ADR — thin Go `tk` over `codebase-memory-mcp`

## Context

v1 spec mandated a `tk`-owned daemon (TCP NDJSON handshake, pid/endpoint files, shutdown deadline), flat `$TK_HOME/~/.tk` layout, and full `mem_*/note_*/ledger_*` stores. That caused Pi close-hangs and duplicated CBM's proven daemon/watcher/store.

## Decision

* Go-only `tk` as shipper: paths + config + one `cbm cli` spawn wrapper + render. No indexer/parser/SQLite touch, no supervisor daemon.
* Linux-style `true-knowledge/` dirs everywhere including macOS (`~/.config`, `~/.local/share`, `~/.cache`, `~/.local/state`); `TK_*`/`TK_HOME` overrides kept for isolated tests.
* CBM coordination daemon is shared per-account (first-starts/last-stops); `cli` mode is daemon-free one-shot. Pi close = stdin EOF = instant exit.
* Indexing default `moderate` (`fast` for watcher/auto, `full` explicit only); `cross-repo-intelligence` only after fresh bases.
* Human CLI via Cobra standard-dynamic completion (projects, config keys, `--client pi,opencode,claude,codex`).
* 27B is the default autonomous target (`scout-7`, 6k budgets); <8B = minimal filter profile only.

## Consequences

* v1 spec frozen as history; `docs/00-AUTHORITY.md` + `CBM-BOUNDARY/PATHS-CONFIG/INDEXING/AGENT-PROFILES` are now truth.
* MVP1 scope: `init/register/index/sync/status/find/explain/grep/arch/mcp/config/migrate/completion` + 7-tool MCP proxy. No facts/notes/ledger/gateway.
