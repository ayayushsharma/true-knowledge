---
id: 2026-09-30-resident-warm-child-needs-no-freshness
title: A resident holds a warm CBM child and needs no freshness machinery
status: authoritative
date: 2026-09-30
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md]
superseded-by: null
---

# A resident holds a warm CBM child and needs no freshness machinery

History is immutable, so this ADR exists to correct
`2026-09-29-measure-latency-not-daemon-routing.md` rather than edit it. Two of
its claims are wrong, both in the direction of over-estimating the difficulty.
Its measurements stand; this supersedes only what it says about **store
lifetime** and about **which boundary rules are untouched**.

## What was wrong

**Wrong: "it registers a session, keeps the store open."** The latency ADR
decided that the persistent child "is a long-lived child of `tk mcp`, not a tk
daemon, so `02-BOUNDARY.md` is untouched."

Both halves fail. The child does not *keep* a store open in any sense that
creates a staleness problem, so the reason the boundary is untouched evaporates
— and a **detached** resident does hold a socket and a long-lived CBM child, so
`AGENTS.md`'s no-supervisor-daemon rule and `02-BOUNDARY.md`'s
no-PIDs-or-endpoints rule are both in play. They will be withdrawn by the ADR
that lands the resident, not by this one, because the resident is not built.

**Wrong: a held child goes stale after another process republishes the store.**
This was the expensive one. It was inferred from CBM `README.md:697`:

> `manage_adr` query modes ... use the server's cached query store ... **If
> another process publishes a replacement store during reindexing, they can
> return the pre-publication ADR until idle eviction refreshes that cache.**

That sentence is scoped to `manage_adr` query modes during a *same-process*
reindex, which is the write path (`mcp.c:10516`, `invalidate_cached_store`; all
seven call sites are write handlers — five in `index_run_supervised`, two in the
ADR paths). `search_graph` does not go through it. Generalising it to "a long
process serves stale data" produced a design with a poller, a goroutine, an
inode-comparison scheme, a generation counter, and a crash window — all of it
unnecessary.

## What was measured

A real CBM 0.11.0 MCP child on Linux, querying every 2s so it never idled, while
`tk index` republished `<CBM_CACHE_DIR>/probe.db` from a separate process,
replacing the inode. The child returned `results: 0` before and `results: 1`
about two seconds after, with no restart. Repeated with `auto_watch = false`,
`auto_index = false`, and a replaced inode: same result every time.

`resolve_store_internal` explains it (`src/mcp/mcp.c:2276`):

```c
srv->store_last_used = time(NULL);
if (srv->current_project && strcmp(srv->current_project, project) == 0 && srv->store) {
    return srv->store;
}
```

The cache is keyed on project and re-resolved on every call. A warm child is
current by construction. Full method, all arms, and the three other wrong
assumptions that grew from the same misreading:
`AGENT_DOCS/THROWAWAY/2026-09-30-warm-child-store-freshness.md`.

## The decision

1. **The resident holds one warm CBM MCP child and does nothing about
   freshness.** No poller, no watcher, no generation counter, no `os.Stat`, no
   invalidation call. CBM re-resolves per call; tk has no part in it. Any future
   proposal to add store-invalidation machinery to tk needs a measurement that
   contradicts the one above, not a restatement of `README.md:697`.
2. **The resident is a transport, not a query layer.** It accepts a tool name
   and arguments, forwards them to the child, and returns CBM's `result` object
   verbatim, so `cbmexec.parseEnvelope` produces a byte-identical `cbmexec.Result`
   on both faces. It is **not** a reuse of `internal/mcp.Server`, which is a
   policy layer that forces `format:"json"` on structured tools and would
   collapse `tk find`'s human table into structured output. The six
   render-parity tests in `internal/cli/cli_test.go` guard this.
3. **Linux and macOS first. Windows is an explicit follow-up, not a phase.** The
   platform split is the existing one in `internal/cbmexec` — `stack_unix.go`
   (`linux || darwin`) beside `stack_other.go` — with one sibling file per
   platform and no build tags at any call site. A named-pipe transport and a
   `CREATE_NO_WINDOW` detach are not designed here. Recorded so that "Windows
   first" is never assumed; a Windows follow-up touches only those files.
4. **A store lives at `<CBM_CACHE_DIR>/<project>.db`; `<repo>/.codebase-memory/` is
   a shareable export, not the query store.** `mcp.c:2036` builds the former;
   `artifact.h:2` calls the latter "export/import for team sharing". tk must not
   watch, stat, or name the repo-local path. This corrects the Artifacts
   paragraph of `05-INDEXING.md`, which described the export as if it were the
   store queries open.
5. **The cache root is per tk home and shared by every tk process under it.**
   `Paths.Cache` is exported as `CBM_CACHE_DIR` for all of them, and CBM's
   cohort is keyed on that root (`main.c:1465`, sha256 of the canonical path). A
   resident's child, `tk index`, and the CBM watcher therefore read the same
   `<cache>/*.db`. The safety of a resident comes from the **shared root**, not
   from isolation, and no cross-cohort race is possible within a home.
6. **`check_index_coverage.freshness` is mtime+size, and you must name the path
   first** (`mcp.c:6305`, `coverage_path_freshness`). Read it as a per-path probe,
   never as a whole-index verdict. Its one known defect, upstream `#1714`, was a
   *Windows-only* mtime-source mismatch: freshness recomputed `mtime_ns` from
   `struct stat`, which truncates to seconds there, while the indexer records
   `cbm_path_info_utf8`'s FILETIME nanoseconds — so on Windows a byte-identical
   file never matched and every path read `metadata_changed`. The fix
   (`455e4fb4`, 2026-08-21) was lost in a merge and re-landed as `#2248` on
   2026-09-20, *after* the `v0.11.0` tag of 2026-09-15 that `internal/config`
   pins — so a binary built from that tag is affected on Windows, while the
   current checkout and every Linux/macOS build are not. tk is Linux and macOS
   first, so the field is sound on tk's target platforms today; it is a reason to
   fix the pin before Windows, not a reason to route around the field.

## Where this leaves the resident

Decided, and all of them simplicity calls:

* `tk mcp --detach` starts a resident and returns. It does not collide with
  `tk daemon`, which is CBM's daemon and stays CLI-only.
* **Explicit start only.** The CLI never auto-spawns a resident. It dials the
  socket; on any failure it falls back to today's one-shot path. A ~4.9s answer
  is the documented degradation, and a dead resident is not an error.
* **No idle timeout.** The resident runs until reboot or a signal. There is no
  `tk session` verb; the socket and a pid file are the whole surface.
* **`tk install cbm` swaps the CBM child underneath a running resident** and
  never kills the resident, which is the process the user is talking to. The
  `resumeDaemon` half is conditional on a daemon having run before, and a
  resident inherits that shape: never start what the user did not ask for. The
  existing `quiesceDaemon` / `resumeDaemon` pair in `internal/cli/admin.go` is
  extended, not duplicated, and stays scoped to tk's own `CBM_RUNTIME_DIR`.
* Writes (`tk index`, `tk sync`) keep the one-shot path. They are not slow
  enough to be worth a resident, and a long-lived process holding the admission
  lease through an index is a worse trade than a 5s write.

## Correcting the record

The concurrency note in `AGENT_DOCS/THROWAWAY` measured that CBM pipelines
`tools/call` and replies out of order. The measurement stands. Its conclusion
for tk does not: `internal/mcp.Server` serves one message at a time, so a
resident built on it has one request outstanding and needs no id demultiplexing,
no in-flight cap, and no child pool. The measured 8-13x is real and unreachable
through tk's server. That note is corrected in place; nothing here contradicts
CBM.

Two upstream items, recorded so they are not rediscovered and not worked around:

* CBM's 60s idle eviction (`mcp.c:124`) is unreachable for a continuously-busy
  client. Measured to be harmless for `search_graph`, since resolution happens
  per call. Worth an upstream issue for the tools that *do* use a long-lived
  cache, of which `manage_adr` is the documented one.
* The CBM checkout at `v0.11.0-219-g80eb92a7` reports `0.11.0` in
  `server.json`, so a version check against the manifest gives a false match.
  Conclusions drawn from that tree need re-validating against a real `0.11.0`
  binary — which is what was done here.

## The generalisable lesson

Three wrong assumptions, one mechanism. Each was a plausible reading of real
evidence, each produced confident work that was thrown away, and two are still
latent in the repo's own docs. `AGENTS.md` now carries the rule: a documented
exception scoped to one tool is not a property of the system; the absence of a
mechanism is not the absence of behaviour; and check the store the queries
actually open before designing around a path.

The rule earned its keep while this ADR was being written. A first draft of
decision 6 called `#1714` a general "mtime truncated to seconds, every path
falsely `metadata_changed`" defect in the pinned binary. Reading the fix showed
the truncation is `struct stat` behaviour, so it is **Windows-only**, and the
fix was re-merged *after* the tag rather than being in it — two different facts
conflated into one alarming one, on a platform tk does not target yet. Same
shape as the three above: a real quote, read past its scope.
