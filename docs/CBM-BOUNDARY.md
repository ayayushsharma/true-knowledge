---
title: CBM boundary — what tk never does
status: authoritative
date: 2026-09-24
supersedes: [compatible-implementation-spec.md §5.2, §5.3, §12, §13, docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md (envelope-first wrapper), docs/DECISIONS/2026-09-24-mcp-inputschema-spec.md (tools/list advertises inputSchema)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# CBM-BOUNDARY — thin `tk` contract

`tk` owns: paths + config + one spawn wrapper + human/JSON render + completion.
CBM owns: parsing, graph, store, daemon, watcher, install matrix, UI, artifacts.

## CBM owns (delegate, never reimplement)

* Tree-sitter AST (162 langs) + Hybrid LSP (10 langs), RAM-first pipeline, FTS5 `cbm_camel_split`, bundled `nomic-embed-code`.
* Store in `${CBM_CACHE_DIR:-~/.cache/codebase-memory-mcp}/`. `tk` never opens `*.db`.
* Coordination daemon: shared per-account, first-starts/last-stops. `cli` mode is daemon-free one-shot (temp worker for `index_repository` only, exits with command).
* Watcher: `mtime+size` poll `1s→60s`, content-hash incremental, non-blocking.
* `install` matrix (45 surfaces) + Scout/Verify/Auditor agents + hooks. `tk` calls `cbm install --dry-run`, never writes client `mcp.json` by hand.
* UI `:9749` (daemon-owned), `.codebase-memory/graph.db.zst` (`Best zstd -9` explicit / `Fast zstd -3` watcher), logs in `${CBM_CACHE_DIR}/logs/`.
* Admission barrier: exact-build + ABI + canonical cache-root. Conflicts → `daemon-conflicts.ndjson`.

## tk owns (and only this)

* XDG `true-knowledge/` path resolution (see `PATHS-CONFIG.md`).
* `config.json` source of truth → propagate via env (`CBM_CACHE_DIR`, `CBM_RUNTIME_DIR`, `CBM_ALLOWED_ROOT`) + `cbm config set`.
* Single spawn wrapper `internal/cbmexec`: `codebase-memory-mcp cli <tool> --args-file <json>` (raw-JSON argv is deprecated upstream) + env + budget truncation + fail-open (`tk install` hint, never block agent). Reads prefer `RunJSON` (`cli --json` envelope unwrapped to text, legacy fallback on any failure). Project required — tk never sends `""`.
* `tk mcp` stdio proxy: `--tool-profile scout(11)|analysis(14)|minimal(3)` filter, snippet + `source_search`. `analysis` adds `query_graph`, `manage_adr` passthrough, `validate`. `index_repository` gated behind explicit approval. `tools/list` advertises every tool with an MCP-spec `inputSchema` (JSON Schema, `type: object` + `properties`/`required`).
* `tk daemon status|stop` is CLI-ONLY: model-facing MCP must never control daemon lifecycle (stopping the shared daemon would strand other agents' watchers). Exists for the documented `watcher_enabled`-flip flow.
* Cobra completion + `--json` + `--help` for human use without agents.

## Bug rule

If `tk` parses source, touches SQLite, manages PIDs/endpoints, or hand-writes agent configs — it's a bug. Route through `cbm cli` / `cbm install`.

If standard Unix commands already do it, tk doesn't reimplement it: no log reader (tail/grep/jq read `tk.log`), no pager, no grep-wrapper. A new tk command needs a reason Unix can't serve.

Refs: CBM `README.md #session-coordination-daemon #cli-mode #auto-index`, `docs/CONFIGURATION.md §2/§4`, `docs/INDEX_RESOURCE_LIMITS.md`, `docs/cbmignore.md`, `server.json`.
