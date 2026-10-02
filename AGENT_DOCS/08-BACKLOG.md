---
id: 08-backlog
title: Backlog — milestones, remaining work, parked and rejected
status: authoritative
date: 2026-09-30
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md, AGENT_DOCS/history/compatible-implementation-spec.md §20, AGENT_DOCS/history/ROADMAP.md, AGENT_DOCS/history/REMAINING-WORK.md, AGENT_DOCS/history/DECISIONS/2026-09-25-fleet-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-rrf-tuning-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-resource-limit-surfacing-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-trajectory-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-delivery-parked-download-scripts.md, AGENT_DOCS/history/DECISIONS/2026-09-24-evals-harness.md]
superseded-by: null
---

# 08-BACKLOG — what is not built, and what never will be

Merged from the old roadmap and remaining-work files. Milestones first, then the
detail, then what is parked and what is permanently rejected.

Locked decisions: Go-only thin `tk` over CBM, Linux-style `true-knowledge/` dirs
everywhere, 27B as the default agent, Cobra-standard completion, cross-repo
deferred. The resident is built; see Open work.

## Status

| Phase | State |
|---|---|
| MVP1 thin proxy + self-installing backends | code complete, verified against real CBM 0.11.0 |
| MVP2 code-intel depth | shipped; cross-repo fleet parked |
| MVP3 memory layer | shipped; e2e in both modes |
| MVP4 human UX + hardening | picker and man pages shipped; evals designed, not built |

Done-when for MVP1 holds: with `TK_HOME=/tmp/x`, `setup → register → index →
sync no-op → status --json → mcp tools/list` (11 tools) passes with zero
`~/.tk` or `Library` writes. MVP2: a 27B answers "who calls X", "what breaks if Y
changes", "outline Z" in at most 3 calls, single-repo. MVP3: facts survive
sessions, notes need approval before they are searchable, the ledger appends
verbatim and trims on retrieval, everything `0600` under the data root. MVP4: a
human completes register → index → explain → sync with tab completion and no
agent present. Order logic: cross-repo rode MVP2 (one extra CBM pass, not a new
store), memory rode MVP3 (stores with review risk), human polish last
(agent correctness gates the rest).

## PARKED INDEFINITELY

Each item needs a new ADR to un-park; the trigger is a revisit condition, not a
promise.

### Cross-repo fleet

Parked: cohort model, the N-source link pass, the generation record, `--cross`
and `--targets`, and `CROSS_*` surfacing in `arch`, `impact`, or `explain`.
Triggers, any one: upstream ships a stable generic cross-repo edge class usable by
a thin client (#56, #398); a workload demands fleet-wide protocol edges; or the
pass becomes effectively free.

Why: N runs per clique after fresh bases, generic cross-repo call graphs blocked
upstream, and flaky matching wherever `Route.path` is empty (FastAPI #678, Go
Fiber #686). Generic multi-repo call graphs are permanently out of tk scope. The
verified per-source contract is in `05-INDEXING.md`, so a re-open is mechanical.

### Fleet-cohort queries

Still parked, narrowed to the CBM-backed verbs: `find`, `grep`, `explain` across
the cohort, each answer coverage-annotated, no CBM cross-repo primitive.
`source-search` answers this for zoekt text today — in-process, spawns nothing —
while these three multiply spawns by N, up to 3N with absence probes. No envelope
shape, `--all`, or `--cohort` surface designed. Triggers: sequential queries
visibly hurting at a given project count, or measured agent demand.

### Result cursor for fleet text search

Parked on reasoning, not evidence. A page token needs a stable total order plus a
per-project `zoekt_head` stamp, refusing rather than serving shifted ranks after a
reindex. It helps a caller that follows protocol — a script, a CI check — not a
model that misreads a long answer, which the scope line addresses. Before
building: run open-ended queries with known answers on an 8B model, scope line on
versus off, and count wrong answers.
`AGENT_DOCS/history/DECISIONS/2026-10-02-cross-repo-fleet-text-search.md`.

### RRF tuning

Parked: `k` and fusion-weight configurability. Today `k = 60`, weight-less, over
an authoritative BM25. Triggers: the evals harness shows `k = 60` misorders
results against BM25-only; embeddings become mandatory rather than optional; a
measured regression ties to `k = 60`; or knobs are demanded with measured gain.
Retrieval quality is a core feature and these knobs are its natural lever — once
evidence exists.

### Resource-limit surfacing

Parked: no `config get` spawn, no `status --json index_limits`, no gap fragment,
no MCP mirror. CBM's `index_max_files` and `index_max_source_mb` default to
`off`, and query tools never surface them — `index_max_*` appears nowhere in
CBM's MCP server code. The only envelope carrying limit values is the
`index_repository` error path. Cap-visible signal today is
`check_index_coverage`'s `not_indexed` and `skipped` counts. Triggers: a capped
repo causing a false absence that coverage could not explain; caps becoming
non-default upstream; systematic cap use at scale; or tk growing in-flight status
where cap state becomes observable for free.

### `trajectory.ndjson`

Parked: a session and state-transition stream beside `tk.log`. Deliberately out
of the evals harness — it is ops, not a benchmark input. Recorded future shape:
one JSONL record per transition, not per invocation, carrying
`{ts, project, op, from_head, to_head, fp_old, fp_new, fresh}` at register,
index, sync-noop, and query, under the same safety contract as the trace log.
Triggers: a support or forensics workflow that needs cross-session lifecycle;
recurring cache or scale post-mortems that per-invocation records cannot answer;
or the evals harness wanting a project-state stream.

### Delivery

Built: the repo-root `install.sh` and `install.ps1`. Each installs tk from a
GitHub release, verifies it against that release's `checksums.txt`, refuses an
asset the manifest does not list, and then runs `tk install cbm`. They hold no
CBM version input — `DefaultCBMPin` stays the only authority, so the scripts
couple tk's release cadence to the CBM pin and that coupling is deliberate.
They exist because tk cannot replace its own running binary.

Still parked: `brew`, `nix`, and `npm` formulas, plus the team `.zst` cadence,
and the release workflow that would publish the platform binaries the two
scripts download. `tk` is unreleased, so that workflow does not exist yet and
the scripts fail cleanly until it does. Triggers: a team actually requiring a
package manager, or a first tagged release.

## Open work

### Spawn overhead — measured, shape decided, not built

A read command takes **~4.9s** on CBM 0.11.0 with TensorFlow indexed, against a
20-100ms target. Not the query (~110ms), not the store (2.3MB costs the same as
686MB), not tk (D ≈ C). It is `nanosleep(1ms)` polling on a
`cbm-version-cohort-*-v1.lock` admission lock that `cli` mode takes on every
spawn and is documented never to need. A long-lived MCP child answers in
**74ms**; a live daemon alone only cuts one-shot to ~1.8s. Method and dead ends:
`AGENT_DOCS/THROWAWAY/2026-09-29-cbm-one-shot-latency.md`.

**Built: one shape, `tk mcp --detach`.** A resident holds one warm CBM MCP
child and listens on a local socket; the CLI dials it and falls back to one-shot
on any failure, so both faces cost the same and a dead resident is not an error.
Explicit start, no auto-spawn, no idle timeout. The resident is a transport, not
a query layer — it forwards to the child and returns CBM's `result` verbatim, so
the two render paths are untouched. Linux and macOS first; Windows is a named
follow-up touching only the socket and detach files. It needs **no** freshness
machinery: a warm child re-resolves the store per call, measured, so there is no
poller, watcher, or generation counter. `tk install cbm` swaps the child
underneath a live resident and never kills it. `02-BOUNDARY.md`'s no-PID/endpoint
rule is narrowed to "a process tk started, in a directory tk chose";
`AGENTS.md`'s no-supervisor rule needs no withdrawal: the resident watches nothing,
restarts nothing, dies on SIGTERM. `.../DECISIONS/2026-09-30-resident-owns-its-endpoint-and-its-swap.md`.

Worth doing first, and independent: `runEnvelope`'s exit-0-no-envelope fallback
re-spawns outside every traced call, so a non-conforming engine costs double the
spawns and `tk.log` records one. Until it is fixed, `tk.log` is a lower bound
and no spawn-count assertion is evidence. Method:
`AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md`.

### Stale-cursor protocol — P3, blocked

tk issues no cursors; every tool is one-shot and bounded by `limit`. A resume
token plus an index-moved `STALE` verdict has no consumer until pagination
exists, and the index-moved decision is the blocker: a tk-issued token must
survive an index generation change, which means deciding what a stale tk token
means when the engine's own offset has silently become invalid. Until that
exists, treat the engine's `has_more` and `next_offset` as the whole pagination
story and do not synthesize a tk cursor on top of them.

### Evals harness — designed, not built

No `tk eval` subcommand, ever; that would be a boundary violation. A committed
frozen fixture in `tests/evals/fixture/` with its git SHA as the pin, a
`fixture.sha` that hard-fails on drift, and a runner `tests/evals/run.sh` that
defaults to fake-CBM with `TK_LIVE=1` opting in. Verdicts are rule-based: `PASS`
= exit 0 plus a required output substring plus a probe-call ceiling; `PARTIAL` =
correct but coverage-dependent, or the ceiling exceeded; `FAIL` = non-zero exit,
a missing artifact, or budget truncation. `est_tokens = chars/4` is a proxy and
must be labeled as one. Journeys for a 27B: who-calls, breakage, orient, grep
route, retention across processes, secret gate, semantic recall; one for a sub-8B
filter. `report.json` is committed as evidence, and drift is a `FAIL`. A
self-judging 27B scorer is a possible phase 2 and must never be the default.

### Text index ignore parity — P3, standing

`source_search` honors neither `.gitignore` nor `.cbmignore`, as documented in
`05-INDEXING.md`. Closing it means reimplementing the engine's filter chain
inside tk, which is forbidden; the narrow alternative, honoring
`.sourcegraph/ignore` only, is still a tk-side filter and needs the same argument
first. `TestIndexDirDoesNotReadGitignore` pins the behavior, so any change is
deliberate and test-visible.

## Permanently rejected

Never re-propose these as "what is next".

* **Real config-file writes in `mcp-install`, and the `pi` client.**
  `setup --client` and `mcp-install` print paste-snippets. Hand-writing client
  `mcp.json` is a boundary bug, and hand-write parity is not being built.
* **`tk history`.** jq reads `tk.log`, and a `--rerun` would re-execute already
  redacted argv.
* **`tk completion-install`.** `tk completion <shell>` prints the script; tk
  never writes a user's rc.
* **`status --watch`.** `watch -n2 tk status` plus `tk status --json` suffice.
* **Generic cross-repo call graphs.** Out of scope permanently; upstream owns them.
