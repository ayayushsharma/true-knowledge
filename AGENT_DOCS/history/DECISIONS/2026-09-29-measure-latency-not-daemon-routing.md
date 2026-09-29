---
title: The daemon cannot serve tool calls, so latency is a spawn-count problem
status: authoritative
date: 2026-09-29
supersedes: null
superseded-by: null
---

# The daemon cannot serve tool calls, so latency is a spawn-count problem

## The complaint

Every `tk` command took over a second, up to eight, and every one of them
invoked `cbm cli`. The proposed remedy was to route tk through the CBM
coordination daemon so the engine could be asked a question over a warm
connection instead of a cold process. That remedy is not available, and
knowing why changed what should be built instead.

## The daemon is not a query server

The CBM README is explicit that the coordination daemon owns *background
services*, and enumerates them: watchers, shared indexing **jobs**, the
optional UI, session registration and cancellation, the admission barrier, and
diagnostics. The store is not in that list. The architecture tree lists
`daemon/` and `store/` as sibling modules, and the one passage about query
caching attributes the handle to "the server" with publication coming from
"another process" — which describes a per-frontend SQLite handle, not a shared
one. Both read paths open the store themselves.

The transport is closed as well. There is no documented unix socket, no
JSON-RPC port, no gRPC, and no `--host` / `--url` / `--connect` flag. The only
HTTP route in the whole document is `POST /api/index` on the UI port `:9749`,
and it triggers an index rather than answering a query. The single occurrence
of the word "socket" in the README is `Socket.IO` graph edges, and the single
occurrence of "IPC" is one line of a directory tree with no transport, no
protocol, and no port attached.

`cli` mode is separate by design, and says so in three places: it "never
starts or connects to the coordination daemon, registers a daemon session, or
starts watchers/UI". So there is no way to ask a running daemon a question, and
a tk-side resident worker is the thing `02-BOUNDARY.md` already bans — "if tk
... manages PIDs or endpoints, it is a bug."

Upstream's "<1ms" claim is the in-process traversal. The README never measures
cold start, which is the number a user actually waits for.

## What the measurement showed

`tests/bench` measures six layers against a real engine, one warmup discarded,
median over N runs. Run on TensorFlow (37k files, 686MB store, CBM 0.11.0):

| layer | median | what it is |
|---|---|---|
| A `cbm --version` | 4.7ms | the spawn floor: exec, link, exit |
| B `cbm cli list_projects` | 4954ms | + the CLI's startup handshake |
| C `cbm cli search_graph` | 5063ms | + a real graph query (~110ms) |
| D `tk find --json` | 4993ms | what a caller waits for |
| E one cbm MCP child, per call | 74ms | the persistent-child shape |

tk's own overhead is ~0: D ≈ C to within noise. The engine's query is ~110ms.
Neither is the problem.

**The 4.9 seconds is a 1ms spin-poll, and it is not the store.** A first
measurement guessed "store open" and was wrong in a way worth recording. A
2.3MB single-file project costs the *same* ~4.9s as the 686MB TensorFlow
store, so the cost cannot be proportional to store size. `strace` finds the
mechanism: **5,708 `nanosleep(1ms)` calls, ≈5.7s of the 4.9s wall clock.**
CBM's `cli` startup takes a *version cohort* admission lock —
`cbm-version-cohort-{admission,lifetime,daemon,maintenance}-v1.lock` in
`<state>/rendezvous/cbm-daemon-<uid>/` — and polls it at 1ms granularity. The
remaining opens are dynamic linking and locale across ~20 processes, not a
repo walk. So the cost is *coordination*, which is the one thing `cli` mode is
documented never to do.

Holding a long-lived MCP child open, which is the daemon-backed path, cuts the
one-shot CLI from ~4.9s to ~1.8s. That is a real 2.7x, and it is why the daemon
was worth wanting: coordination is genuinely the cost. But 1.8s is still 18x
over budget, because the handshake is only part of it. The child itself answers
in **74ms**, so the persistent child is the only shape that reaches the target.

## Two findings the measurement forced

**A spawn that the trace log does not record.** Layer F counts engine
executions two ways: the events in `tk.log`, and ground truth from a counting
wrapper interposed on `TK_CBM_BIN`. They disagreed. A `tk find` whose engine
did not answer `--json` cost two spawns and logged one — the exit-0-no-envelope
fallback in `runEnvelope` re-runs the tool through `Run` outside every traced
call. `tk.log` is therefore a *lower bound* on spawns, and a latency budget
built by reading it under-counts by exactly the call nobody recorded. Making
the fake honor `--json` and `format:"json"` dropped D from 201ms to 132ms and
spawns from 2 to 1, which locates the cost precisely.

**The router can silently change which question is asked.** `tk find` sends a
query containing regex metacharacters to `search_code` and a bare identifier to
`search_graph`. Benchmarking `live.Demo` measured grep, not the graph, because
the `.` is a metacharacter. A latency comparison across two layers is only
valid when both layers asked the same question; the harness now pins a bare
identifier and reports the route the trace log recorded.

## The decision

1. **No daemon transport, so none is built.** The persistent child is an MCP
   stdio server (`cbm` with no args), which is the daemon-*backed* path — it
   registers a session, keeps the store open, and holds the warm state the
   spawn was throwing away. This is a long-lived child of `tk mcp`, not a tk
   daemon, so `02-BOUNDARY.md` is untouched.
2. **Measure before optimizing, and keep the measurement.** `mise run bench`
   and `mise run bench-real` ship as a tool, not a `tk` command — the Unix
   rule in `02-BOUNDARY.md` is why there is no `tk bench` verb. A perf claim
   without a layer breakdown is an opinion.
3. **The untraced fallback is a defect now, independent of any latency work.**
   It doubles the spawns for any engine that does not honor `--json`, and the
   trace log hides it. It gets its own fix and its own ADR; it is recorded
   here because the measurement found it.
4. **A fake engine cannot answer this question.** `08-BACKLOG.md` and
   `09-CHECKLIST.md` rule 8 already say a fake is not evidence at scale. The
   first pass of this work used a fake with *known injected* costs to validate
   the harness, and the real run contradicted the fake's diagnosis — the fake
   said "store open" and the real engine said "coordination handshake". The
   harness shipped anyway and was right; the explanation it produced was not.

## What is now measured, and what is not

Every figure above is from CBM 0.11.0 on a real indexed repository, and it
answers the question the work was commissioned to answer: **a read command
takes ~5s, not the 20-100ms target — a factor of ~50.** It also localises the
cost precisely enough to act on: coordination handshake, not query, not store,
not tk.

Still open, and cheap to check before writing code:

* Whether the 1ms poll interval is a tunable upstream, or whether a healthy
  daemon collapses the handshake entirely. A healthy daemon gave 1.8s, so
  "entirely" is unlikely, but the poll's floor is unknown.
* Whether the admission wait is contention with a live daemon or a fixed
  cost. The measurement cannot distinguish them, and the fix differs: a
  contention bug is upstream's, a fixed cost is simply amortised by keeping
  the child.
* Whether 74ms holds on Windows and on a 12-core-loaded host. It is one
  sample on one machine.
