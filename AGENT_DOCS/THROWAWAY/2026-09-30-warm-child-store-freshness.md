# Does a warm CBM MCP child serve stale graph results?

**Status: measured — no.** CBM 0.11.0, Linux, 2026-09-30. Real engine, real
scratch repo, a real long-lived MCP child. The question was going to decide the
whole resident design, so it was measured rather than read.

## Answer

A warm child picks up a republished store **without idling, without a restart,
and with the watcher and auto-index both disabled.** The design consequence is
large and negative: the resident needs no freshness machinery at all.

## The measurement

A real `cbm mcp` child, handshaked, then querying every 2s so it never idled.
Meanwhile `tk index` republished `<CBM_CACHE_DIR>/probe.db` from a separate
process, replacing the inode.

```
EpsilonFive committed, NOT indexed.   probe.db inode=9212

02:26:19  EpsilonFive  results: 0    ┐
02:26:21  EpsilonFive  results: 0    │
02:26:23  EpsilonFive  results: 0    │  warm child, store held,
02:26:25  EpsilonFive  results: 0    │  not one idle second
02:26:27  EpsilonFive  results: 0    ┘
>>>>>>>> tk index runs >>>>>>>        inode 9212 -> 9437
02:26:29  EpsilonFive  results: 0
02:26:31  EpsilonFive  results: 0
02:26:33  EpsilonFive  results: 1    <-- fresh, ~2s later
02:26:35  EpsilonFive  results: 1
```

Repeated, and every arm returned the same thing:

| condition | child sees the new index |
|---|---|
| never idle (2s cadence) | yes |
| `auto_watch = false` | yes |
| `auto_index = false` | yes |
| inode replaced by rename | yes |
| one-shot control process | yes (trivially) |

## Why — and why the code read said otherwise

`resolve_store_internal` is **not** a permanent handle. `src/mcp/mcp.c:2276`:

```c
srv->store_last_used = time(NULL);

/* Already open for this project? */
if (srv->current_project && strcmp(srv->current_project, project) == 0 && srv->store) {
    return srv->store;
}
```

The cache is keyed on project and re-resolved on every call. The measured
behaviour matches this. I read it the other way.

## Three wrong assumptions, and where each came from

Recorded because all three produced confident design work that had to be thrown
away, and two of them are still latent in the repo.

### 1. A cached store pins the old inode

**Believed because:** CBM's `README.md:697` says a query "can return the
pre-publication ADR until idle eviction refreshes that cache". Read as a general
hazard. It is scoped to `manage_adr` query modes during a **same-process**
reindex, which goes through `invalidate_cached_store` on the write path
(`mcp.c:10516`, seven call sites, every one a write path). `search_graph` does
not use that path.

**The tell I skipped:** the sentence names its own remedy as *idle eviction*,
and `mcp.c:124` sets `STORE_IDLE_TIMEOUT_S 60`. If the mechanism is idleness,
then a client that never idles is a case the design does not cover — which
should have been a reason to test it, not to assume the worst. Instead of
testing, I built an invalidation subsystem around it.

**Cost of the assumption:** ~60 lines of poller, a goroutine, a ticker, an
inode-comparison scheme, a `gen` counter, and a new failure mode (crash between
stat and respawn). All deleted.

### 2. `README.md:697` is the contract, so no revalidation exists

**Believed because:** CBM documents **no** client-callable staleness check.
`index_status` returns `indexed_at`/`nodes`/`edges` and self-describes as
"Best-effort only" (`mcp.c:679`). `check_index_coverage` is per-path mtime+size
(`mcp.c:6337`) and you must name the path first. `cbm_store_generation()`
(`store.h:454`) exists and is exposed to nothing.

All true. The inference was wrong: the absence of a *staleness check* is not the
absence of *staleness handling*. CBM handles it internally, by re-resolving.

### 3. The live store is repo-local

**Believed because:** the Artifacts paragraph of `AGENT_DOCS/05-INDEXING.md`
(then at lines 155-156) said an explicit index writes `Best` and the watcher
writes `Fast`, "both to `.codebase-memory/graph.db.zst`". That sentence
describes the artifact **export**, and tk's own doc repeated it. Two different
files:

```
live store    <CBM_CACHE_DIR>/<project>.db      mcp.c:2036  project_db_path()
                                                   what every query opens
shareable     <repo>/.codebase-memory/
              graph.db.zst                      artifact.h:2 "for team sharing"
                                                   derived; nobody queries it
```

Confirmed on disk: after `tk index`, `probe.db` is in the cache and the scratch
repo contains no `.codebase-memory/` at all.

**Cost of the assumption:** I nearly had tk watch a repo path — a new coupling,
a reversal of `05-INDEXING.md`'s daemon-owned-watcher line, and a boundary
question about touching CBM's artifacts — to detect a change in a file **tk
already owns and already exports as `CBM_CACHE_DIR`**.

## A fourth: the cache is per-process, not per-home

**Believed because:** I called the per-`TK_HOME` cache "tk's own cohort" and
treated it as isolation. Measured:

```
TK_HOME=/tmp/opencode/tkhome     -> tkhome/cache/probe.db
TK_HOME=/tmp/opencode/tkhome2    -> tkhome2/cache/probe2.db
~/.cache/codebase-memory-mcp/    -> _config.db only, no project db
```

One cache root per tk home, **shared by every tk process under that home**. Every
`tk` invocation resolves the same `Paths.Cache` and exports the same
`CBM_CACHE_DIR`, so a resident's child, `tk index`, and the watcher all read the
same `<cache>/*.db`. CBM's cohort is keyed on that root (`main.c:1465`, sha256 of
the canonical cache path).

The isolation is not what makes a resident safe. The **shared root** is. The
words "own cohort" were wrong and would have led to a design that reasoned about
cross-cohort races that cannot happen.

## Method, so it can be repeated

* Scratch repo under `/tmp`, one `.go` file per symbol, `git commit` per symbol.
* A Go probe speaking raw JSON-RPC to a real `cbm mcp` child: `initialize`,
  `notifications/initialized`, then `tools/call` on a loop.
* A symbol is added and committed but **not** indexed. `T0` must return 0.
* `tk index` runs in a **separate process** while the child keeps querying.
* `T1` must return 1. Anything else is a stale child.
* Control: the same query through `cbm cli search_graph --json`.

## The lesson, generalised

**A documented exception scoped to one tool is not a property of the system.**
`README.md:697` is scoped to `manage_adr`. Read it as a global invariant and it
sounds like a hazard; read it as a local note and it is trivia. The scope word
was in the sentence and was skipped both times it was read.

**Absence of a mechanism is not absence of behaviour.** "CBM exposes no
staleness API" and "CBM serves stale data" are different claims, and only the
first was evidenced.

**Check the store the queries open.** One grep of `project_db_path` would have
prevented a whole branch of design.

## Caveats

* Linux, one build, one small repo. The re-resolve is in `mcp.c` and has no
  platform branch, but macOS and Windows are unmeasured here.
* `search_graph` only. The re-resolve sits in `resolve_store`, which is shared,
  so every project-scoped tool should behave alike — but that is inference until
  a second tool is measured.
* The child was a bare MCP server. A resident's lifecycle (retire, respawn after
  `tk install cbm`) is unbuilt, so the swap-underneath path is unexercised.
