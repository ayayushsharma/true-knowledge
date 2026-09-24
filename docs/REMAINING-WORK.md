---
title: Remaining work — the deferred/parked backlog (survives code compaction)
status: authoritative
date: 2026-09-25
supersedes: []
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

**Not parked (separable, open/unprioritized):** fleet-cohort **queries** —
run the existing `find`/`grep`/`explain` across the registered cohort with
coverage-before-absence annotation, using **no** CBM cross-repo primitive.
Because it delegates to existing single-repo machinery (`annotateAbsence` +
`freshness`), it is a candidate slice if fleet-wide "search the whole
registry" value is ever wanted.

```
today                      fleet queries (unprioritized, no CBM cross-repo)
projA graph ──┐             projA graph ──┐
projB graph ──┼→ find X     projB graph ──┼→ find X → coverage-annotated,
projC graph ──┘  one repo   projC graph ──┘   per-repo results across cohort
```

## 2. Ops (P2) — trajectory, resource surfacing; delivery parked

```
state/
  logs/tk.log         per-invocation trace        (exists)
  trajectory.ndjson   session/state-transition log (missing)
status --json         freshness                    (exists)
                      index limits                (missing)
delivery              dist/tk                     (exists)
                      brew/nix/npm + .zst cadence (PARKED — delivery ADR)
```

- **`trajectory.ndjson`**: session-level state-transition stream
  (register→index→query→re-index with head/fingerprint deltas), distinct from
  tk.log's per-invocation records; deliberately OUT of the evals harness
  (`docs/DECISIONS/2026-09-24-evals-harness.md:47`). Schema needs a small ops ADR.
- **Resource-limit surfacing**: CBM caps (`index_max_files/mb`, 512MiB,
  fail-whole-preserve-serving) exist (INDEXING.md:51); tk exposes none —
  `status --json` and search envelopes should surface caps/degradation.
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

## 4. RRF tuning (P3) — memory-layer deferred

```
note search fusion      BM25 ─┐
                        cosine─┤ RRF k=60 hard-coded (memory/embed.go:120,146)
                        fused ─┘ ✗ k/weights not configurable
```

Ledger compaction/re-anchor is **resolved by design** — the ledger is
append-only JSONL with a retrieval-only budget and a human-only `prune`
(`docs/DECISIONS/2026-09-25-ledger-append-only-history-prune.md`); nothing
truncates or rewrites stored entries, so anchors can never be dropped.

Remaining: **RRF tuning** — promote `k=60` and fusion weights to config keys
(use the `ui.picker` plumbing pattern). Cosmetic; ROADMAP:47.

## 5. Stale-cursor protocol (P3, blocked)

No tk tool returns continuations; all are one-shot bounded by `limit`
(INDEXING.md:61, ROADMAP:33 — "no tk-issued cursors exist yet"). Protocol
(resume token + index-moved → STALE → re-query mandate) has no consumer until
pagination is built. Lowest priority.

```
one-shot (now):                  with cursors (future):
search → ≤limit rows             search → results + cursor
  no continuation                resume → next window
  "absence" from top-20          index moved → STALE → "re-query"
```