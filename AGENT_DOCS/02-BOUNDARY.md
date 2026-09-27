---
id: 02-boundary
title: The CBM boundary — what tk must never do
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/CBM-BOUNDARY.md, AGENT_DOCS/history/DECISIONS/2026-09-24-mcp-inputschema-spec.md, AGENT_DOCS/history/DECISIONS/2026-09-24-log-redaction-profile-gate-cancellation.md, AGENT_DOCS/history/DECISIONS/2026-09-26-structured-cbm-payloads.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

> Plain English on purpose. A compressed boundary rule is a misread waiting to
> happen, and a misread here writes to the wrong database.

# 02-BOUNDARY — the thin-client contract

`tk` is a shipper. It owns paths, configuration, exactly one spawn wrapper,
rendering, and completion. Everything else belongs to CBM. This file is written
in plain English because a rule that gets misread here corrupts data or
duplicates an engine.

## CBM owns these. Delegate, never reimplement

* Parsing and the graph: Tree-sitter across 162 languages, Hybrid LSP across 10,
  the RAM-first pipeline, FTS5 with `cbm_camel_split`, bundled
  `nomic-embed-code`.
* The store, in `$CBM_CACHE_DIR` (default `~/.cache/codebase-memory-mcp`).
* The coordination daemon: shared per account, first-starts and last-stops.
  `cli` mode is daemon-free and one-shot.
* The watcher: `mtime+size` poll from 1 second on small trees to 60 seconds on
  large ones, content-hash re-parse of changed files only, non-blocking.
* The install matrix across 45 client surfaces, plus the Scout, Verify, and
  Auditor agents and their hooks.
* The UI on port `9749`, the `.codebase-memory/graph.db.zst` artifacts, and CBM's
  own logs.
* The admission barrier: exact build, ABI, and canonical cache root, with
  conflicts written to `daemon-conflicts.ndjson`.

## tk owns these, and only these

* XDG `true-knowledge/` path resolution. See `06-PATHS-CONFIG.md`.
* `config.json` as the source of truth, propagated to CBM through
  `CBM_CACHE_DIR`, `CBM_RUNTIME_DIR`, and `CBM_ALLOWED_ROOT`, plus
  `cbm config set`.
* One spawn wrapper, `internal/cbmexec`, with environment propagation, budget
  truncation, and fail-open behavior. When CBM is down, tk prints a
  `tk install` hint and lets the agent continue. tk must never block an agent.
* Three things tk adds to a payload it merely forwards: freshness fields, an
  absence verdict, and budget markers. Nothing else.
* The `tk mcp` stdio proxy: profile filtering, `inputSchema` advertisement,
  per-request timeouts, and answer-cancellation.
* The memory layer: `tk mem`, `tk note`, `tk ledger`. tk-owned, in-process, and
  never dependent on CBM.
* Cobra completion, `--json`, and `--help` so a human needs no agent.

## Hard rules

**Never open a CBM `*.db`.** If you catch yourself writing SQL against
`_config.db` or any engine store, stop. Use `cbm cli`.

**Never write a client `mcp.json` by hand.** Use `cbm install --dry-run` or
`tk install` / `tk mcp-install` to see what the installer would do. tk prints
snippets; the installer writes.

**Never reimplement a filter chain that CBM already owns.** The text index is
the standing example: `source_search` does not honor `.gitignore` or
`.cbmignore`, and that divergence is left standing on purpose. Two filter stacks
drift, and the second one is the one nobody tests. See `05-INDEXING.md`.

**Never re-model or re-render a CBM payload.** Pass it through. Do not scrape
text to manufacture structure, do not slice JSON to fit a budget, and do not
substitute a tk schema for the engine's. `04-MCP.md` has the exact contract.

**Never control the daemon from MCP.** `tk daemon` is CLI-only. Stopping the
shared daemon would strand every other agent's watcher.

**Never validate a CBM tool's arguments.** CBM validates them server-side. tk
gates profiles and wraps errors, nothing else. tk-owned memory tools validate
their own arguments in-process.

**Never rebuild the world on save.** `tk sync` is a no-op on a clean tree, and
the watcher handles the rest. Forcing a full index on every edit is what the
doctrine forbids.

**Never claim absence without evidence.** An empty result is only absence when
an engine counter says zero, and an absence from a gated read must carry
`check_index_coverage`'s verdict. A failed probe is a hard error, never a
silent absence.

## The Unix rule

If a standard Unix command already does the job, tk does not reimplement it.
There is no log reader, because `tail`, `grep`, and `jq` read `tk.log`. There is
no pager. There is no `grep` wrapper. A new tk command needs a reason that Unix
cannot serve. Rejected on exactly this ground: `tk history`, `tk
completion-install`, and `status --watch` — use `jq`, wire your own rc, and
`watch -n2 tk status`.

## Bug rule, one line

If tk parses source, touches SQLite, manages PIDs or endpoints, or hand-writes
agent configuration, it is a bug.

Upstream references: CBM `README.md` sections `#session-coordination-daemon`,
`#cli-mode`, `#auto-index`; CBM `docs/CONFIGURATION.md` §2 and §4; CBM
`docs/INDEX_RESOURCE_LIMITS.md`; CBM `docs/cbmignore.md`; `server.json`.
