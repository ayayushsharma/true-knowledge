---
title: Indexing — modes, discovery, watcher, zoekt text index
status: authoritative
date: 2026-09-25
supersedes: [compatible-implementation-spec.md §7, docs/DECISIONS/2026-09-24-zoekt-staleness.md (source-search freshness contract), docs/DECISIONS/2026-09-24-log-redaction-profile-gate-cancellation.md (ctx cancellation)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# INDEXING — how CBM indexing works, what `tk` defaults to, and the Zoekt text index

## Zoekt text index (linked library, not a backend)

Explicit trigram text search, never a silent fallback — served in-process
via `internal/zoekttext` (same-language links, cross-language spawns; see
zoekt-library ADR). Shards live at `<cache>/zoekt/<project>/`;
`tk index`/`tk sync` build via `gitindex.IndexGitRepo` incremental (git
repos — the returned `updated` bool is the free no-op signal) or the `index`
builder (plain dirs). No ctags, no servers: the no-daemon doctrine holds.

Measured during the spawn spike (zoekt @153817f6, behavior transfers 1:1):
cold index ~5s at k8s scale (29k files), incremental re-run instant,
queries tens of ms. Library form additionally drops the ~42ms fork/exec tax
and the base64 JSONL round-trip (structs out), and upgrades timeouts to
context cancellation. `tk status --json` reports per-backend `head` +
`zoekt_head`, and source-search is never allowed to serve stale shards:
before answering it refreshes to `git HEAD` (incremental no-op ~1ms on a
clean tree, ~0.7s delta index on a real change), samples dirty-worktree hits
from live disk bytes, and flags worktree drift in-band (see zoekt-staleness
ADR). Structurally stale shards (pre-ADR legacy shape) still route to CBM
`search_code`. Mandatory guards: import side-effect audit per pin, `recover()`
middleware at handler boundaries, size caps before every index call.
Builds are `IsDelta: true`: normal-build shards for the base, tiny delta
shards appended on change, automatic normal fallback when a delta cannot
apply.

## Modes (no `slow`; `full` is the slow one)

| Mode | Files | Similarity/semantic edges | tk use |
|---|---|---|---|
| `fast` | filtered (skips `docs/examples/testdata`, archives/media/lockfiles/`.min.js`) | skipped | watcher / auto-index default |
| `moderate` | filtered (same) | on | `tk index` default (explicit) |
| `full` (CBM default) | all (safety skips only) | on | `tk index --full` only, CI nightly |
| `cross-repo-intelligence` | no extraction | on (protocol edges only) | per-**source** pass, after fresh bases, `target_projects=["*"]` → `CROSS_*` edges; N sources link an N-repo clique (contract ADR) |

Perf anchors: avg repo `ms`, Django `~6s (49k nodes)`, kernel `full 3min / fast 1m12s`. Queries: Cypher `<1ms`, search/trace `<10ms`.

## Discovery order (fixed)

Built-in skip (`.git`/`node_modules` core non-negatable) → root `.gitignore`+`info/exclude` → nested `.gitignore` → root `.cbmignore` (can un-skip layer-1 except core) → global `core.excludesFile`. Then suffix filters + `512 MiB` single-file cap + optional `index_max_files/mb` (fail-whole-preserve-serving, never partial publish). Symlinks always skipped.

## Watcher (why edits stay fast)

Daemon-owned `mtime+size` poll (`1s` small → `60s` huge), content-hash re-parse of changed files only (`~4x` faster than full), non-blocking (skips cycle if manual index runs). Knobs via `tk config` → `cbm config`: `auto_index`, `auto_index_limit=50000`, `auto_watch=true`, `watcher_enabled=true` (read once at daemon start → needs `daemon stop`).

Artifacts: explicit index = `Best (VACUUM INTO + zstd -9)`; watcher = `Fast (zstd -3)` to `.codebase-memory/graph.db.zst`. Default: gitignore artifact; commit deliberately (release cadence), never every save.

## tk freshness contract

`tk sync` = `git HEAD` (or mtime fingerprint for plain dirs) + `check_index_coverage` → clean = no-op, dirty = let watcher do it. Check coverage before negative claims. Query responses carry `head/current/fresh`; stale cursors deferred (no tk-issued cursors exist yet).
