---
title: source-search is never stale — auto-refresh + live worktree bytes + delta indexing
status: authoritative
date: 2026-09-24
supersedes: [docs/DECISIONS/2026-09-23-explicit-source-search.md (serving model: static shards only)]
superseded-by: null
---

# ADR — zoekt source-search staleness is a bug, not a caveat

## Context

Proven against `/home/ayush/projects/personal/tensorflow` (37k files, 565MB,
plain local clone, 12 cores): zoekt shards served by `source-search` /
`source_search` could silently lie three independent ways, all reproducible
with a marker token across two commits:

1. **Commit staleness.** After committing a change without `tk sync`,
   `source-search` returned `(no matches)` for the new token and served the old
   commit's bytes; MCP `source_search` returned a silent `(no matches)`.
2. **Dirty worktree** (`fresh:true` + `clean z:ok` while a token existed only in
   the worktree): shards cover HEAD, so a worktree-only symbol `(no matches)`
   with `fresh:true` — an agent could wrongly conclude the symbol does not exist.
3. **Shard bytes ≠ disk.** Results echoed the committed line while the worktree
   held `<<EDITED-BY-WORKTREE>>` content.

Latency (tensorflow): full reindex 18.5–18.9s; delta on one file ~0.7s;
incremental no-op ~1ms (984µs); `git status --porcelain` ~68ms; `git rev-parse`
~3–5ms. The staleness was never worth the rebuild cost — false negatives in
scout profile are paid by every agent session.

## Decision

Serve from the truth cheaply. Three mechanisms, smallest surface first:

* **Auto-refresh** (`ensureZoektAndTouch`, called by CLI `source-search` and by
  the MCP server before `source_search`): if the covered `zoekt_head` != `git
  HEAD`, reindex. Clean HEAD is a near-free no-op via zoekt's
  `IncrementalSkipIndexing`; a real change reindexes to match `git HEAD` before
  answering, and the registry `Head/ZoektHead/Fingerprint` is updated only when
  the covered HEAD actually changes (never on worktree dirt alone).
* **Delta indexing** (`IndexRepo` now passes `IsDelta: true`): one-line change
  that turns "rewrite 800MB of shards" into "append a ~2KB delta shard"
  (measured: 706ms on tensorflow vs 18.5s). zoekt falls back to a normal build
  automatically when a delta can't apply (hg/submodule layouts/sub-zero
  thresholds); existing normal-build shards keep working unmodified. No cleanup:
  zoekt shard GC is CBM's job.
* **Dirty worktrees are annotated, never force-rebuilt.** A worktree ahead of
  HEAD is *deliberately* not indexed — rebuilding the worktree on save would
  cost 30–60s+ per editor keystroke and betray `.gitignore`/`.cbmignore`
  discipline. Instead, `source-search`/`source_search`:
  - slice match lines from **live disk bytes** (`SearchLive`) whenever
    `--project-root`/`ProjectRoot` is available, so results show what's on disk
    (files deleted in the worktree keep serving, tagged `(worktree-missing)`),
    then re-reslice each hit at its current line number;
  - prepend a one-line `[source-search: N modified, N untracked in worktree not
    indexed]` note when `git status --porcelain` (`gitx.Dirty`) reports dirt, so
    a miss is honest instead of a false negative;
  - add JSON fields `zoekt_head`, `zoekt_fresh`, `worktree_modified`,
    `worktree_untracked` to the CLI envelope (`freshness()` in root.go grew the
    zoekt side; it is registry-reads only — no `git status` in generic status
    output).
  - `gitx.Dirty` TTL-caches its counts (~2s, process-local, errors never
    cached): a burst of searches from several agents sharing one `tk mcp` server
    pays `git status` once per beat, not per search (~68ms at 37k files per
    status). `git rev-parse HEAD` is deliberately NOT cached — commit
    visibility must stay immediate. The annotation may lag edits by ≤2s; the
    live byte slice is always current.
* **Fail-open.** A failed auto-refresh (`reindex boom`, locked index, missing
  git) still searches the shards it has and prepends the failure note; the MCP
  tool never errors out on refresh trouble. `EnsureIndex`/`Staleness`/
  `ProjectRoot` are nil-safe `Server` hooks — a bare `Server{}` keeps its old
  static-shard behavior for tests and embedded uses.
* Revert seam preserved: `internal/zoekttext` still exposes exactly four entry
  points (`IndexRepo`, `IndexDir`, `Search`, `SearchLive`); callers never reach
  `gitindex`/`index` directly, so a future swap (e.g. incremental CBM) is one
  file.

Out of scope (explicitly rejected): worktree full-rebuild on save, watching
worktrees, MCP cursors for search, embedding anything.

## Consequence

* `source-search`/`source_search` are now weakly consistent: they answer only
  after refreshing to `git HEAD` and they show what's on the disk for dirty
  files. The remaining hole is an edit *during* a search — in-process zoekt
  holds the lock, so a save mid-search still misses until the next call; that
  is the correct fail-conservative direction.
* Reindex cost moved into the hot path: clean = ~1ms, committed-and-unsynced =
  ~0.7s (delta, tensorflow), structural layout changes = a full normal build
  (~19s) — the same cost `tk sync` always charged, now only paid when
  answering. Budgets/truncation already cap the response on the MCP side.
* Registry writes happen whenever a covered HEAD changes; the fingerprint stays
  stable across pure-worktree edits so `tk status` and watcher affinity are
  unaffected.
* Unit/e2e coverage: `gitx.Dirty` (clean/modified+untracked/staged/non-git),
  `SearchLive` (live slice, shard-oldness, missing-file tag), MCP hooks
  (refresh-once, live snippet, staleness note, fail-open keep-searching, nil
  hooks still pass the pre-existing round-trip tests).