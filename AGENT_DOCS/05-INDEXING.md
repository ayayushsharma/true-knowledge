---
id: 05-indexing
title: Indexing — modes, discovery, watcher, Zoekt text index, freshness
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/compatible-implementation-spec.md §7, AGENT_DOCS/history/INDEXING.md, AGENT_DOCS/history/DECISIONS/2026-09-23-explicit-source-search.md, AGENT_DOCS/history/DECISIONS/2026-09-23-zoekt-library-not-backend.md, AGENT_DOCS/history/DECISIONS/2026-09-24-zoekt-staleness.md, AGENT_DOCS/history/DECISIONS/2026-09-25-comments-pass-fixes.md, AGENT_DOCS/history/DECISIONS/2026-09-25-cross-repo-contract-verified.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

# 05-INDEXING — modes, discovery, freshness

## Modes

| Mode | Files | Similarity/semantic edges | tk use |
|---|---|---|---|
| `fast` | filtered: skips `docs/`, `examples/`, `testdata/`, archives, media, lockfiles, `*.min.js` | off | watcher and auto-index default |
| `moderate` | same filter set | on | `tk index` default |
| `full` (CBM default) | all, safety skips only | on | `tk index --full` only, CI nightly |
| `cross-repo-intelligence` | no extraction | protocol edges only | per-source pass, see below |

There is no `slow` mode. `full` is the slow one.

Perf anchors: an average repository takes milliseconds; Django about 6 seconds
at 49k nodes; the kernel takes about 3 minutes at `full` and 1m12s at `fast`.
Queries: Cypher under 1 ms, search and trace under 10 ms.

## Discovery order (fixed, engine-side)

Built-in skip, where `.git` and `node_modules` are non-negotiable → root
`.gitignore` plus `info/exclude` → nested `.gitignore` → root `.cbmignore`,
which can un-skip a layer-1 path but never the core → global
`core.excludesFile`. Then suffix filters, a 512 MiB single-file cap, and the
optional `index_max_files` / `index_max_source_mb`. Exceeding a cap fails the
whole index attempt and preserves the serving database — never a partial
publish. Symlinks are always skipped.

## The Zoekt text index

Explicit, never a silent fallback. `tk source-search` is text and only text;
`tk find` and `tk grep` are CBM. One name never silently means the other
backend. A missing index produces a hard error naming the fix: `run tk index`
for absent shards, `run tk setup` when Zoekt is unavailable.

Served in-process by `internal/zoekttext` — no server, no ctags, no
`zoekt-webserver`, no per-platform sidecar. Shards live at
`<cache>/zoekt/<project>/`; `zoekt_head` is tracked in `tk.json`.

| Entry point | Use |
|---|---|
| `IndexRepo` | git repositories, incremental via `gitindex.IndexGitRepo`; the returned `updated` bool is the free no-op signal |
| `IndexDir` | plain directories |
| `Search` | trigram query under a 60 s context-bounded searcher, structs out, no base64 round-trip |
| `SearchLive` | slices match lines from live disk bytes, then re-slices each hit at its current line number |

Callers must not reach `gitindex` or `index` directly. Those four functions are
the only seam.

Mandatory safety gates, re-checked on every pin bump: an import side-effect
audit of `query`, `index`, `gitindex`, `shards`, and the root `zoekt` package,
`recover()` middleware at the MCP handler boundary and index paths, and size
caps before every index call.

### Source search is never stale

Three silent-lie modes were closed: commit staleness, a dirty worktree reported
as fresh, and shard bytes that no longer match disk.

* Auto-refresh before answering: reindex when the covered `zoekt_head` differs
  from `git HEAD`. A clean tree costs about 1 ms; a real change costs about 0.7 s
  as a delta shard; a structural layout change falls back to a normal build of
  roughly 19 s.
* Builds are `IsDelta: true`, so a change appends a small delta instead of
  rewriting everything. Zoekt falls back to a normal build when a delta cannot
  apply, and existing normal-build shards keep working unmodified. Shard GC is
  CBM's job, not tk's.
* A dirty worktree is annotated in-band, never force-rebuilt:
  `[source-search: N modified, N untracked in worktree not indexed]`.
  Rebuilding on save would cost 30–60 s per keystroke.
* `Head`, `ZoektHead`, and `Fingerprint` update only when the covered HEAD
  actually changes, never on worktree dirt alone.
* `gitx.Dirty` caches counts for about 2 s, process-local, and never caches
  errors. `git rev-parse HEAD` is never cached, so commit visibility stays
  immediate. An edit can therefore lag the annotation by up to 2 s, while the
  live byte slice is always current.
* A failed refresh still searches what it has and prepends the failure note.
  The MCP tool never errors on refresh trouble. `EnsureIndex`, `Staleness`, and
  `ProjectRoot` are nil-safe hooks, so a bare `Server{}` keeps static-shard
  behavior.

### Plain-directory filters

`IndexDir` walks the whole tree minus an always-skipped core set:
`.git`, `.hg`, `.svn`, `node_modules`, `vendor`, `venv`, `.venv`,
`__pycache__`, `.tox`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.next`,
`.nuxt`. These are unconditional. Lockfiles are deliberately not skipped: they
are small, exactly pinned, and useful search targets.

On top of the core set, the optional `<config>/ignore` applies a gitignore-lite
pattern list to plain directories only: `#` for comments, a leading `/` anchors
to the project root, a trailing `/` targets directory subtrees, and any other
line matches one component at any depth. No glob magic. A missing file means no
custom filters. Git-repository indexes ignore this file entirely.

### What the text index does *not* honor

This is a standing, documented divergence, not an oversight.

* Only `.sourcegraph/ignore` is an explicit ignore file for git-repository
  indexes. `.gitignore` applies only implicitly, because ignored files are
  untracked and therefore absent from the commit tree that `gitindex` walks. A
  **committed** `node_modules/`, `dist/`, or `*.min.js` is skipped by CBM and
  still indexed by tk.
* Plain directories get neither `.gitignore` nor `.cbmignore`. `<config>/ignore`
  is the only knob.
* CBM's 512 MiB cap, `index_max_*`, and suffix filters do not apply to the text
  index.
* What tk does enforce itself, because these are correctness rather than policy:
  non-regular files and symlinks are never read — a FIFO would block the walk
  forever, and a symlink would be indexed under its target's bytes — and files
  above Zoekt's `SizeMax` of 2 MiB are recorded as `SkipReasonTooLarge` without
  ever being buffered, so an oversized file costs no RAM.

Closing this divergence would mean reimplementing the engine's filter chain
inside tk, which `02-BOUNDARY.md` forbids. `TestIndexDirDoesNotReadGitignore`
pins the current behavior, so a change here is deliberate and test-visible.

## Cross-repo mode

`cross-repo-intelligence` is an `index_repository` mode, not a separate tool,
and it is per **source** project. `target_projects=["*"]` expands the target list
only. An N-repository clique needs **N runs**, each with every member as source,
each after fresh bases, each wiping and rebuilding that source's `CROSS_*` edges
bidirectionally. Preconditions are hard failures, not skips: the source must
already be indexed and every named target must validate and exist. Upsert key
`(source_id, target_id, type)`, so repeat runs converge.

Edge types are protocol edges only: `CROSS_HTTP_CALLS`, `CROSS_ASYNC_CALLS`,
`CROSS_CHANNEL`, `CROSS_GRPC_CALLS`, `CROSS_GRAPHQL_CALLS`, `CROSS_TRPC_CALLS`.
Generic cross-repo function call edges do not exist yet — upstream issues #56
and #398. Matcher holes are extractor-driven: an empty `Route.path` yields zero
edges, which is the FastAPI #678 and Go Fiber #686 case.

Fleet orchestration is parked indefinitely; see `08-BACKLOG.md`.

## The watcher

Daemon-owned, which is why tk has none. `mtime+size` poll, 1 s on small trees up
to 60 s on large ones, content-hash re-parse of changed files only, about 4×
faster than a full re-parse, and non-blocking: it skips a cycle when a manual
index is running. Knobs reach CBM through `tk config` then `cbm config`:
`auto_index`, `auto_watch`, `watcher_enabled`, and CBM's `auto_index_limit`
(50000). `watcher_enabled` is read once at daemon start, so flipping it needs
`tk daemon stop` first.

Artifacts: an explicit index writes `Best (VACUUM INTO + zstd -9)`, the watcher
writes `Fast (zstd -3)`, both to `.codebase-memory/graph.db.zst`. The default is
the gitignore artifact. Commit deliberately on a release cadence, never on every
save.

## The tk freshness contract

`tk sync` means **both backends are fresh**. A git repository is clean when
`HEAD == head && zoekt_head == HEAD`. A plain directory is clean when its
fingerprint matches **and** `zoekt_head == "files"`. A project indexed before
Zoekt existed is dirty and gets a text-index backfill on the next sync. No
recorded state is ever reported clean while `source-search` would serve a stale
index.

Clean is a no-op. Dirty means the watcher handles it. Query responses carry
`head`, `current`, and `fresh`. Check coverage before any negative claim. tk
issues no cursors.
