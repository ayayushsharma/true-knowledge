# Why a tk read takes ~5s when the engine claims <1ms queries

**Status: measured.** CBM 0.11.0, Linux, TensorFlow indexed (37k files, 686MB
store), median of 10, one warmup discarded. Reproduce: `mise run bench-real`.

## The numbers

| layer | median | what it is |
|---|---|---|
| `cbm --version` | 4.7ms | the spawn floor: exec, link, exit |
| `cbm cli list_projects` | 4954ms | + the CLI's admission handshake |
| `cbm cli search_graph` | 5063ms | + a real graph query (~110ms) |
| `tk find --json` | 4993ms | what a caller waits for |
| one long-lived MCP child, per call | **74ms** | the persistent-child shape |

tk's own overhead is ~0 — D ≈ C to within noise. The query is ~110ms. Neither
is the problem.

## The mechanism, and how it was nearly got wrong

The first explanation was "store open". **It is wrong.** A 2.3MB single-file
project costs the same 4.9s as the 686MB TensorFlow store, so the cost cannot
scale with store size.

`strace` finds it: **5,708 `nanosleep(1ms)` calls, ≈5.7s of a 4.9s wall
clock.** The other opens are dynamic linking and locale across ~20 processes,
not a repo walk — `tf.db` is opened exactly once.

The locks in `<state>/rendezvous/cbm-daemon-<uid>/` name the culprit:
`cbm-version-cohort-{admission,lifetime,daemon,maintenance}-v1.lock`.

## Why the engine does this on purpose

Upstream `#cli-mode`, quoted:

> CLI tools neither start nor connect to the coordination daemon and leave no
> standing process behind. They hold a crash-safe exact-build admission lease
> **only for the command lifetime**.

And `#session-coordination-daemon`:

> MCP servers, hooks, one-shot CLI commands, temporary index workers, and the
> daemon share a crash-safe OS admission barrier.

So a one-shot CLI acquires the admission lease, spawns a temporary monitor,
runs, releases. The 4.9s is lease acquisition. **This is specified behaviour,
not a bug**, which is why "file it upstream" is a weak lever: the 1ms poll
granularity may be improvable, but one-shot mode is meant to be separate from
the daemon, and a session that does not pay it cannot be one-shot.

## What a live daemon buys

Holding a CBM MCP child open (which is the daemon-backed path) drops one-shot
CLI from 4.9s to **1.8s**. Real, 2.7x — coordination really is the cost. But
1.8s is still 18x over budget, because the handshake is only part of it. Only
the child itself reaches 74ms.

## The floor

Splitting the one-shot number: B (no real query) 4954ms, C (query) 5063ms, so
the query is ~109ms cold — but the same query through a warm child is 74ms.
Per-process caches are part of it. So ~74ms is close to the irreducible cost
of a graph search, and the 20-100ms target has little margin on this call.
Cheaper tools should be well under it; **not yet measured individually.**

## Two measurement traps that produced wrong numbers

**`tk.log` under-counts spawns.** `runEnvelope` re-runs a tool outside every
traced call when the engine does not honour `--json` (exit-nonzero path and
exit-0-no-envelope path). A non-conforming engine costs 2 spawns, logs 1.
`tk.log` is a lower bound, so any budget read from it is wrong by exactly the
call nobody recorded. Fixed measure, unfixed defect.

**`tk find` can silently ask a different question.** A query containing regex
metacharacters routes to `search_code`; a bare identifier routes to
`search_graph`. `live.Demo` measures grep, not the graph. Two layers are only
comparable when both asked the same thing — pin a bare identifier and read the
route the trace recorded.

**A third, harness-only:** CBM resolves its store from `CBM_CACHE_DIR` /
`CBM_RUNTIME_DIR`. A layer that execs the binary without tk's env measures an
empty database and reports "project not indexed". `tests/bench` now mirrors
`cbmexec.Runner.env`.

## Unanswered

* Which exact lock each `nanosleep` polls, and whether 5,708 is a fixed retry
  budget or contention. Needs a strace that attributes sleeps to the preceding
  `openat` path, run with and without a live daemon. 5,708 is suspiciously
  near a round number, which would mean no daemon can ever make one-shot fast.
* Whether 74ms is the floor across tools, not just `search_graph`.
* Windows, and a loaded host. One machine, one session, one engine build.
