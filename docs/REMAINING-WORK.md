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

## 1. Cross-repo fleet (P1, parked, needs ADR)

Single-repo queries only today; fleet = symbol/CALL edges that span the
registered set (`CROSS_*`), fleet-wide absence claims.

- tk has zero fleet code: no `--cross/--targets`, no link pass, no generation
  record (`docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md:53-56`).
- CBM primitive exists: one `cross-repo-intelligence` call, `target_projects=["*"]`,
  `get_architecture` reports `cross_repo_links` free (INDEXING.md:45).
- To build: cohort model (registry as fleet vs explicit `--targets`), link pass
  after *fresh* bases, **generation record** (SHA-cohort provenance → staleness),
  surface `CROSS_*` in `arch`/`impact`/`explain` envelopes. Needs a fleet ADR first.

```
today                      fleet (goal)
projA graph ──┐             projA graph ──┐
projB graph ──┼→ find X     projB graph ──┼→ find X → CROSS_* edge
projC graph ──┘  one repo   projC graph ──┘   → repo+symbol
```

## 2. Ops + packaging (P2) — four independent sub-items

```
state/
  logs/tk.log         per-invocation trace        (exists)
  trajectory.ndjson   session/state-transition log (missing)
status --json         freshness                    (exists)
                      index limits                (missing)
delivery              dist/tk                     (exists)
                      brew/nix/npm + .zst cadence (missing)
```

- **`trajectory.ndjson`**: session-level state-transition stream
  (register→index→query→re-index with head/fingerprint deltas), distinct from
  tk.log's per-invocation records; deliberately OUT of the evals harness
  (`docs/DECISIONS/2026-09-24-evals-harness.md:47`). Schema needs a small ops ADR.
- **Resource-limit surfacing**: CBM caps (`index_max_files/mb`, 512MiB,
  fail-whole-preserve-serving) exist (INDEXING.md:51); tk exposes none —
  `status --json` and search envelopes should surface caps/degradation.
- **Packaging**: brew/nix/npm formula sources for the static binary.
- **Team `.zst` cadence**: distribution schedule for committed `graph.db.zst`
  artifacts (CBM-owned); tk surfaces artifact age/last-built in `status`.

## 3. Scout coverage-before-absence (P2) — smallest honest gap

CLI paths already enforce: `find`/`grep` → `annotateAbsence` on empty
(`internal/cli/query.go:118,202`), verdict via `check_index_coverage`
(`internal/cli/validate.go:26`). The **MCP scout profile** does NOT:

```
find/grep   empty → coverage check → CLI annotates   ✅ query.go:118,202
scout tools empty → search_graph/search_code served bare ❌ mcp/server.go:431,544
```

Fix = mirror `annotateAbsence` inside the scout handlers. No new tools, no
scout tool-count change, byte-consistent with the CLI. ROADMAP:33 "left open".

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