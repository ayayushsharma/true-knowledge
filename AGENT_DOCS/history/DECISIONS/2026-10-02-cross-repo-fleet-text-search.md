---
id: 2026-10-02-cross-repo-fleet-text-search
title: Fleet text search is an explicit opt-in behind --all-projects, and every result states its own completeness
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md]
superseded-by: null
---

# Fleet text search is explicit, and every result states what it withheld

Supersedes `2026-09-25-fleet-cohort-queries-parked-indefinitely.md`, which
parked cohort queries for `find`/`grep`/`explain` — the CBM-backed verbs, where a
cohort multiplies spawns by N. This lands the other half: `source-search` is
zoekt, in-process, no spawn, so the cost argument that parked the CBM half does
not apply to it. The CBM cohort stays parked; `08-BACKLOG.md` keeps it, narrowed
to the verbs it actually covers.

## A directory searcher is not a tree searcher

The throwaway note this replaces asserted that `NewDirectorySearcher` loads every
shard below a directory, so one searcher already spans repositories, and proposed
a `ShardsAll` hook returning `<cache>/zoekt`. That is false, and it would have
shipped a fleet search that answers `(no matches)` forever with no error.

The load path is a flat glob, `filepath.Join(dir, "*.zoekt")`
(`search/watcher.go:141`), and the fsnotify watch is `watcher.Add(s.dir)` on that
same one directory (`search/watcher.go:234`). tk keeps shards one level deeper,
`<cache>/zoekt/<project>/` (`internal/paths/paths.go:131-135`). Measured against
the pin with two projects in tk's own layout:

```
per-project <cache>/zoekt/alpha -> matches=1 repos=[alpha] err=<nil>
PARENT     <cache>/zoekt        -> matches=0 repos=[]     err=<nil>
missing    <cache>/zoekt/nope   -> matches=0              err="open: no such file or directory"
```

`repo:^(alpha|beta)$` against the parent also returned 0 matches and no error.
The filter was never the problem: there is no loaded query to attach it to. A
missing directory errors, but an *existing* directory whose entries are all
subdirectories loads zero shards and returns empty. That is the exact silent
absence `AGENTS.md` rule 4 exists to catch, and the only reason it was caught
here is that the claim was measured rather than read off the constructor's
signature.

The capability is real, just not under that name: every hit is tagged with its
repository (`api.go:39-44`), and a result is expected to span repositories
(`api.go:527-541`). So the fleet is **N per-project directory searchers in
process**, walked serially. Ten small projects cost 7ms cold and 21ms warm. The
objection to a fan-out was never latency; it is that N verdicts answer one
question.

## Explicit, because this command never guessed

`source-search` is the one command with no routing magic: no backend guessing
(`2026-09-23-explicit-source-search.md`), no silent fallback
(`05-INDEXING.md:39`). Making absence mean "the whole registry" would be the
same class of move — the caller's intent inferred rather than stated — and it
would be invisible in the invocation.

So the fleet is opt-in: `--all-projects`, or the MCP `all_projects` argument.
`--project` wins; both together is an error; none of the three is an error that
names all three routes. `resolveProject`'s single-project auto-select
(`internal/cli/query.go:19`) keeps serving the other ten project-resolving
commands untouched.

This also dissolves the prefix question. Hits are prefixed `repo:file:line:`
only under the fleet, so the shape of the answer reveals how it was scoped, and
a one-project registry needs no special case because the fleet is never implied.

## Every result says what it withheld

This is the load-bearing part, and it exists because of who reads the output. A
small model handed 20 hits and a bare `...truncated` concludes the answer is
complete. Handed the same 20 hits and `214 matches in 87 files; returned top 20;
truncated`, it has a decision to make. Confidence rises with volume while
correctness does not, so volume is the wrong variable; an explicit statement of
completeness is the fix.

```
[source-search: 214 matches in 87 files across 3 projects (alpha, beta, gamma);
returned top 20 from 12 files; truncated — raise --limit or narrow --files;
delta skipped (no text index); epsilon not searched (limit)]
```

Three withheld states, never merged, because merging them is how a caller reads
"truncated" as "nothing else matched":

* **truncated** — the limit fired; matches exist and were not returned.
* **skipped** — a project that could not be searched, with the reason.
* **not searched** — a project the walk never reached, because the budget was
  already spent. Only ever reported under `--all-projects`.

This is a **scope and completeness** statement. It is not a coverage claim, and
`04-MCP.md` keeps saying so: an empty fleet result is absence only where the
per-project freshness check supports it, exactly as for one project. The two
rules are adjacent and must not be confused.

## The totals are free, and `--limit 0` is not unbounded

zoekt has no `Offset` (`api.go:975-1040`), so a cursor over results is not a
parameter tk can pass. `SearchOptions.SetDefaults` rewrites
`TotalMaxMatchCount == 0` to `10 * ShardMaxMatchCount` — one million
(`api.go:1045-1053`). tk does not call `SetDefaults` on search options
(`internal/zoekttext/zoekttext.go:274`), so `--limit 0` passes through as a
genuine unbounded request, but nothing about that makes infinity a reportable
answer. The completeness line carries the real count or says nothing.

`TotalMaxMatchCount` alone does not bound the result set. Measured on a
10k-file repo with every file matching, set to 20:

```
TotalMaxMatchCount=20 only -> matches=10000 files=10000 took=38ms
+MaxDocDisplayCount=20     -> matches=   20 files=   20 took=42ms
+MaxMatchDisplayCount=20   -> matches=   20 files=   20 took=29ms
```

tk's own loop was truncating after the fact (`zoekttext.go:285`), so the
per-project heap cost was every match, not `limit` matches. `MaxDocDisplayCount`
is the cap to add: it bounds files and matches together, and file count is
reported.

Truncating the display does not destroy the count. `index/limit.go`'s
`DisplayTruncator` mutates the aggregated slice after collection, returns
`hasMore`, and `Stats.MatchCount` / `Stats.FileCount` (`api.go:373-425`) are
filled per shard during search (`api.go:461`) — independent of any display cap.
So the line reports the uncapped total and the truncated returned count side by
side, from one search. `hasMore` is preferred over `len(matches) == limit` as
the truncation test: it is exact when both caps are set and removes the guess
when they are not.

`MatchCount` counts line *fragments*, not lines (`index/limit.go:110` decrements
by `len(lm.LineFragments)`), so a multi-fragment line contributes more than one
to the total while rendering one hit. The line says "matches" meaning zoekt's
counter, the same convention CBM envelopes already use. Not reconciled, because
reconciling it would make the number mean something other than what the engine
reported.

## Freshness: keep the incremental call

Per project, the fleet calls `ensureZoektAndTouch` exactly as one project does.
Measured on a 10k-file repo: cold build 1.195s, incremental no-op 2-3ms. The
throwaway note proposed replacing that with a HEAD compare plus one `stat`,
saving about 3ms per project. Rejected: `gitindex` self-heals a broken shard or
delta chain that a `stat` would trust, and the saving is noise against the
search itself.

The fleet drops one thing the single-project path does not: the `git status`
half of the staleness note. Measured cost is 3ms on a small repo and about 68ms
at 37k files (`internal/gitx/git.go:24-25`) — it scales with repo size, it is the
most expensive part of a fleet walk, and per-project worktree counts are the
least load-bearing fact in a scope annotation. Committed drift still annotates.

## Serial, no pool, streamed

No worker pool. Each searcher sizes its scheduler to `GOMAXPROCS`
(`search/sched.go:49`, `search/shards.go:224-231`), so running several
concurrently oversubscribes the cores it is trying to use, and merging by
completion order breaks the deterministic `(project, rank)` order that any future
cursor would need. A pool would also be the first supervisor-shaped thing in a
codebase with an explicit no-supervisor boundary, for no measured gain: 500
projects cost about 22s at roughly 45ms each (search plus freshness plus four
git spawns), and only under `--limit 0` — the default `--limit 20` early-stops
inside a handful of projects.

Memory is bounded by streaming rather than by concurrency. Each project's group
is rendered as it completes, so peak heap is the largest single project, not the
fleet total. `defer searcher.Close()` sits inside a per-project closure: a defer
in a loop body runs at function exit, which would hold N fsnotify watchers open
in the long-lived resident child.

## Grouping restores what tk discards

`internal/zoekttext/zoekttext.go:283-295` walks `res.Files` — already grouped
per file, already ranked — and flattens every line match. Boundaries and
per-file counts are gone. Twenty hits in one function and twenty hits across
twenty files render identically, and a caller cannot tell which situation it is
looking at. That distinction is the difference between "drill into this file"
and "search more broadly", and it is the signal a small model actually uses.

Fleet-only. `--project a` stays byte-identical, because the human surface is the
one thing the pre-release law protects and the dual-path tests already hold it
to.

## Not built

**A result cursor.** No `Offset` in zoekt, so it needs a stable total order plus
a per-project `zoekt_head` stamp, and it must refuse rather than serve shifted
ranks after a reindex. It helps a caller that follows protocol — a script, a CI
check — and not a model that misreads a large answer, which is the problem the
completeness line already addresses. Parked in `08-BACKLOG.md` with its trigger.

**Per-project naming of character-budget truncation.** The budget is per call,
so late projects can be cut from an already-rendered result, and the marker does
not name them. Deliberate: it lengthens the one line small models must parse, and
`--limit 0` callers who care about a specific project have `--project`. Revisit
with the cursor if it becomes a real complaint.

**Grouped rendering for `--project`.** Would need a recorded concession against
the byte-identical invariant.

**Parallel search.** See above.

**Validation.** The claim that the completeness line helps small models is a
mechanism argument, not a measurement. Run open-ended queries with known answers
on an 8B class model, annotation on versus off, and count wrong answers. Until
that runs, the cursor stays parked on reasoning rather than evidence, and the
`08-BACKLOG.md` entry says so.