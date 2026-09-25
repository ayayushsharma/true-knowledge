---
title: Remaining work — the deferred/parked backlog (survives code compaction)
status: authoritative
date: 2026-09-26
supersedes: [docs/DECISIONS/2026-09-26-structured-cbm-payloads.md (structured CBM passthrough; §5 stale-cursor narrowed)]
superseded-by: null
---

# REMAINING-WORK — what is not yet built (and what will never be)

> Backlog companion to `docs/ROADMAP.md`. ROADMAP states *milestones*; this file
> records *remaining items in detail* so none is lost when code is compacted.
> Authority: newest DECISIONS > this file > ROADMAP on overlap; this file
> conflicts with nothing — it only expands roadmap bullets with citations.

Permanently rejected (never re-propose as "what's next"):

* **`tk mcp-install` real config-file writes + `pi` client** — a non-MVP
  proposal. `setup --client`/`mcp-install` keep printing paste-snippets
  (`internal/cli/setup.go:19-49`, `internal/cli/admin.go:454`). Writing client
  `mcp.json` by hand stays a CBM-boundary bug (CBM-BOUNDARY.md:22,37); hand-write
  parity is NOT being built.
* **`tk history` / `tk completion-install` / `status --watch`** — already
  rejected in `docs/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md`
  (Unix-does-it doctrine, `internal/trace/trace.go:6`, CBM-BOUNDARY.md:39).

---

## 1. Cross-repo fleet (PARKED INDEFINITELY, trigger-based)

**Decision: `docs/DECISIONS/2026-09-25-fleet-parked-indefinitely.md`** — tk
fleet orchestration for `CROSS_*` edges (cohort model, N-source link pass,
generation record, `--cross/--targets`, `CROSS_*` envelopes) is parked
indefinitely. Revisit only on a trigger: upstream generic cross-repo edge
classes land (#56/#398), a concrete user workload needs fleet-wide
protocol-edge links, or the pass becomes effectively free. Un-parking needs a
new ADR. Generic multi-repo call graphs stay permanently out of tk scope
(CBM-BOUNDARY); they live or die upstream.

The verified upstream contract stands (for reuse when/if un-parked):
`cross-repo-intelligence` is a per-**source-project** `index_repository` mode
(`docs/DECISIONS/2026-09-25-cross-repo-contract-verified.md`) — `["*"]` expands
only the *targets*; a full N-repo clique needs **N runs (every member as
source)**, each after fresh bases, each wiping-and-rebuilding that source's
`CROSS_*` edges bidirectionally. Scope is protocol edges only
(HTTP/async/channel/gRPC/GraphQL/tRPC); matcher holes exist where `Route.path`
is empty (FastAPI #678, Go Fiber #686). `get_architecture` reports
`cross_repo_links` free (INDEXING.md:45).

**Fleet-cohort queries — PARKED INDEFINITELY too** per
`docs/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md`: the
"search the whole registry" slice (existing single-repo
`find`/`grep`/`explain` across the cohort, coverage-annotated, **no** CBM
cross-repo primitive) is parked with the fleet — same trigger doctrine, no
envelope shape designed, value scales with repo count and there is no
consumer. Revisit alongside the fleet triggers or on concrete multi-repo
workload/explicit cohort demand. §1 is therefore **fully parked**.

```
today                      fleet queries (PARKED INDEFINITELY — cohort ADR)
projA graph ──┐             projA graph ──┐
projB graph ──┼→ find X     projB graph ──┼→ find X → coverage-annotated,
projC graph ──┘  one repo   projC graph ──┘   per-repo results across cohort
```

## 2. Ops (P2) — fully parked (trajectory + resource surfacing + delivery)

```
state/
  logs/tk.log         per-invocation trace        (exists)
  trajectory.ndjson   session/state-transition log (PARKED — trajectory ADR)
status --json         freshness                    (exists)
                      index limits                (PARKED — resource-surfacing ADR)
delivery              dist/tk                     (exists)
                      brew/nix/npm + .zst cadence (PARKED — delivery ADR)
```

- **`trajectory.ndjson` — PARKED INDEFINITELY** per
  `docs/DECISIONS/2026-09-25-trajectory-parked-indefinitely.md`: the
  session/state-transition stream (register→index→query→re-index with
  head/fingerprint deltas) is ops debt with no consumer and a flat design
  cost — the schema direction (one JSONL record per transition, not
  invocation; trace's safety contract) is recorded for a mechanical re-open.
  Deliberately OUT of the evals harness (`docs/DECISIONS/2026-09-24-evals-harness.md:47`).
- **Resource-limit surfacing — PARKED INDEFINITELY** per
  `docs/DECISIONS/2026-09-25-resource-limit-surfacing-parked-indefinitely.md`:
  surfacing CBM caps (`index_max_files`/`index_max_source_mb`, default `off`,
  512MiB per-file, fail-whole-preserve-serving) in `status --json` / search
  envelopes is parked. Verified upstream: **query tools never surface caps** —
  `index_max_*` appears nowhere in MCP server code; only the `index_repository`
  error envelope carries limit values; `check_index_coverage`'s
  `not_indexed`/`skipped` counts are the cap-visible signal today. Revisit on a
  trigger (a capped repo causing an unexplained false-absence report, caps
  becoming non-default upstream, systematic cap use at scale, or cap state
  becoming observable for free). Un-parking needs a new ADR.
- **Delivery — PARKED INDEFINITELY** per `docs/DECISIONS/2026-09-25-delivery-parked-download-scripts.md`:
  brew/nix/npm formulas and the team `.zst` ship cadence are deferred
  (revisit only on package-manager/tabular demand). The recorded future path,
  deliberately not built yet: platform binaries + `checksums.txt` on the
  GitHub release page fetched by simple `install.sh` / `install.ps1` download
  scripts (curl / Invoke-WebRequest, latest-or-pinned tag). `.zst` artifacts
  stay CBM-owned as today; `tk install` (CBM installer) is unaffected.

## 3. Scout coverage-before-absence (SHIPPED)

**Decision: `docs/DECISIONS/2026-09-25-scout-coverage-before-absence.md`** —
enforcement now applies at the tool level (all profiles): `search_graph` /
`search_code` empty results trigger a whole-project `check_index_coverage`
probe and append the verdict, byte-consistent with the CLI
(`internal/mcp/server.go`, `annotateAbsence` shared with `callValidate`);
probe failure on empty results is a hard error envelope (never silent
absence). No new tools — profile counts unchanged (11/14/3/22).

```
find/grep   empty → coverage check → CLI annotates   ✅ query.go:118,202
scout tools empty → search_graph/search_code annotated ✅ mcp/server.go
```

## 4. RRF tuning (PARKED INDEFINITELY — future scope)

**Decision: `docs/DECISIONS/2026-09-25-rrf-tuning-parked-indefinitely.md`** —
`k`/fusion-weight configurability is parked indefinitely. Today's fusion is
canonical RRF `k=60` (`memory/embed.go:120,146`) over an authoritative BM25
with embeddings as a fail-open enhancement — there is no evidence of
misordering and no evals harness to judge one, so knobs without measurement
are config-for-config's-sake.

```
note search fusion      BM25 ─┐
                        cosine─┤ RRF k=60 hard-coded (memory/embed.go:120,146)
                        fused ─┘ P k/weights: PARKED (future scope)
```

**Future scope, explicitly:** retrieval quality is a core feature and the knobs
are its natural lever once evidence exists. Revisit when any trigger fires
(evals harness built + shows misordering, embeddings become mandatory/critical
path, measured regression tied to `k=60`, or evidence-backed demand). Un-parking
needs a new ADR.

Ledger compaction/re-anchor stays **resolved by design** — the ledger is
append-only JSONL with a retrieval-only budget and a human-only `prune`
(`docs/DECISIONS/2026-09-25-ledger-append-only-history-prune.md`); nothing
truncates or rewrites stored entries, so anchors can never be dropped.

## 5. Stale-cursor protocol (P3, blocked)

No tk tool returns continuations; all are one-shot bounded by `limit`
(INDEXING.md:61). Protocol (resume token + index-moved → STALE → re-query
mandate) has no consumer until pagination is built. Lowest priority.

> **Refinement (structured-payloads ADR, 2026-09-26).** This item is narrower
> than it looked. CBM *already* paged — its payloads carry `has_more`,
> `next_offset` and `truncation_reason` — and tk now passes those through
> verbatim on the payload face, so a client can walk results with an **engine**
> offset today. What tk does not do is issue a cursor of its own, and that is
> the only part still blocked: a tk-issued token has to survive an index
> generation change, which means deciding what a stale tk token means when the
> engine's own offset has silently become invalid. Until that decision exists,
> treat the engine's `has_more` as the whole pagination story and do not
> synthesise a tk cursor on top of it.

```
one-shot (now):                  with cursors (future):
search → ≤limit rows             search → results + cursor
  no continuation                resume → next window
  "absence" from top-20          index moved → STALE → "re-query"
```