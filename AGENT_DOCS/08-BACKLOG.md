---
id: 08-backlog
title: Backlog — milestones, remaining work, parked and rejected
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/compatible-implementation-spec.md §20, AGENT_DOCS/history/ROADMAP.md, AGENT_DOCS/history/REMAINING-WORK.md, AGENT_DOCS/history/DECISIONS/2026-09-25-fleet-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-rrf-tuning-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-resource-limit-surfacing-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-trajectory-parked-indefinitely.md, AGENT_DOCS/history/DECISIONS/2026-09-25-delivery-parked-download-scripts.md, AGENT_DOCS/history/DECISIONS/2026-09-24-evals-harness.md]
superseded-by: null
---

# 08-BACKLOG — what is not built, and what never will be

Merged from the old roadmap and remaining-work files. Milestones first, then the
detail, then what is parked and what is permanently rejected.

Locked decisions: Go-only thin `tk` over CBM, Linux-style `true-knowledge/`
dirs everywhere, no supervisor daemon, 27B as the default agent, Cobra-standard
completion, cross-repo deferred.

## Status

| Phase | State |
|---|---|
| MVP1 thin proxy + self-installing backends | code complete, verified against real CBM 0.11.0 |
| MVP2 code-intel depth | shipped; cross-repo fleet parked |
| MVP3 memory layer | shipped; e2e in both modes |
| MVP4 human UX + hardening | picker and man pages shipped; evals designed, not built |

Done-when for MVP1 holds: with `TK_HOME=/tmp/x`, `setup → register → index →
sync no-op → status --json → mcp tools/list` (11 tools) passes with zero
`~/.tk` or `Library` writes.

Done-when for MVP2: a 27B answers "who calls X", "what breaks if Y changes", and
"outline Z" in at most 3 calls, single-repo. Fleet-wide edges are parked.

Done-when for MVP3: facts survive sessions, notes need approval before they are
searchable, the ledger appends verbatim and trims on retrieval, and everything is
`0600` under the data root.

Done-when for MVP4: a human completes register → index → explain → sync with tab
completion everywhere and no agent present.

Order logic: cross-repo rode MVP2 because it is one extra CBM pass, not a new
store. Memory rode MVP3 because it adds stores with review risk. Human polish
rode last because Cobra-standard is already usable and agent correctness gates
the rest.

## PARKED INDEFINITELY

Each item needs a new ADR to un-park, and the trigger is a revisit condition,
not a promise.

### Cross-repo fleet

Parked: cohort model, the N-source link pass, the generation record, `--cross`
and `--targets` flags, and `CROSS_*` surfacing in `arch`, `impact`, or `explain`
envelopes. Triggers, any one: upstream ships a stable generic cross-repo edge
class usable by a thin client (#56, #398); a concrete user workload demands
fleet-wide protocol edges; or the pass becomes effectively free.

Why: N runs per clique after fresh bases, generic cross-repo call graphs blocked
upstream, and flaky matching wherever `Route.path` is empty (FastAPI #678, Go
Fiber #686). Generic multi-repo call graphs are permanently out of tk scope —
they live or die upstream. The verified per-source contract is recorded in
`05-INDEXING.md` so a re-open is mechanical.

### Fleet-cohort queries

Parked with the fleet: running existing `find`, `grep`, and `explain` across the
registered cohort, each answer coverage-annotated, with no CBM cross-repo
primitive. No envelope shape, `--all`, or `--cohort` surface was designed. Cost
multiplies spawns by N, up to 3N with absence probes. Triggers: a real
multi-repo "search the whole registry" workload, a registered-project count where
sequential queries visibly hurt, or measured agent demand.

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

Parked: `brew`, `nix`, and `npm` formulas, plus the team `.zst` cadence. The
`.zst` artifacts themselves stay CBM-owned and unchanged; only tk's cadence is
parked. Triggers: a team actually requiring a package manager, or verified
demand for `.zst` age surfacing. Recorded direction, deliberately unbuilt:
GitHub release page, platform binaries plus `checksums.txt`, fetched by simple
`install.sh` (curl, sha256sum) and `install.ps1` (Invoke-WebRequest,
Get-FileHash), latest-or-pinned tag. `tk install`, the CBM installer, is
unrelated and unaffected.

## Open work

### Stale-cursor protocol — P3, blocked

tk issues no cursors; every tool is one-shot and bounded by `limit`. A resume
token plus an index-moved `STALE` verdict has no consumer until pagination
exists, and the index-moved decision is the blocker: a tk-issued token must
survive an index generation change, which means deciding what a stale tk token
means when the engine's own offset has silently become invalid. Until that
decision exists, treat the engine's `has_more` and `next_offset` as the whole
pagination story and do not synthesize a tk cursor on top of them.

### Evals harness — designed, not built

No `tk eval` subcommand, ever; that would be a boundary violation. A committed
frozen fixture in `tests/evals/fixture/` with its git SHA as the pin, a
`fixture.sha` that hard-fails on drift, and a runner `tests/evals/run.sh` that
defaults to fake-CBM with `TK_LIVE=1` opting in. Verdicts are rule-based: `PASS`
= exit 0 plus a required output substring plus a probe-call ceiling; `PARTIAL` =
correct but coverage-dependent, or the ceiling exceeded; `FAIL` = non-zero exit,
a missing artifact, or budget truncation. `est_tokens = chars/4` is a proxy and
must be labeled as one. Journeys for a 27B: who-calls, breakage, orient, grep
route, retention across processes, secret gate, semantic recall. One for a
sub-8B filter. `report.json` is committed as evidence, and drift is a `FAIL`. A
self-judging 27B scorer is a possible phase 2 and must never be the default.

### Text index ignore parity — P3, standing

`source_search` honors neither `.gitignore` nor `.cbmignore`, as documented in
`05-INDEXING.md`. Closing it means reimplementing the engine's filter chain
inside tk, which is forbidden; the narrow alternative, honoring
`.sourcegraph/ignore` only, is still a tk-side filter and needs the same
argument first. `TestIndexDirDoesNotReadGitignore` pins the behavior, so any
change is deliberate and test-visible.

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
* **Generic cross-repo call graphs.** Out of scope permanently; upstream owns
  them.
* **Multi-repo "search the whole registry"** — parked above, and parked for the
  same trigger doctrine as the fleet.
