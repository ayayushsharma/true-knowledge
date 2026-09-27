---
title: Fleet-cohort queries parked indefinitely — the speculative half of the fleet
status: authoritative
date: 2026-09-25
supersedes: [docs/REMAINING-WORK.md §1 ("Not parked (separable, open/unprioritized): fleet-cohort queries")]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park fleet-cohort queries

## Context

When the cross-repo fleet was parked (`docs/DECISIONS/2026-09-25-fleet-parked-indefinitely.md`),
one slice was left open: **fleet-cohort queries** — run the existing
single-repo `find`/`grep`/`explain` across the registered cohort, each answer
coverage-before-absence annotated, using **no** CBM cross-repo primitive. It
is the boundary-safe half of "search the whole registry".

Why parking is right today:

1. **No consumer.** Nothing calls "search every registered repo". tk is a
   one-shot thin proxy per project; a cohort command multiplies spawns by N
   (2N–3N worst case with absence probes) and invents a
   per-project-results+coverage envelope shape tk has never produced —
   real budget/timeout semantics work (fail-whole vs partial) for a
   speculative hot path.
2. **Value scales with repo count.** For the workstation case (a handful of
   registered projects) per-project queries are already comfortable; the
   cohort's value arrives only at fleet scale, which in turn only arrives with
   the parked fleet.
3. **Zero feasibility risk, zero urgency.** It delegates to existing machinery
   (`annotateAbsence` + `freshness`); nothing about it can go stale, so the
   design cost stays flat.

## Decision

Park fleet-cohort queries **indefinitely**, folding them into the fleet park
doctrine (trigger-based). Revisit via a new ADR — alongside the fleet
triggers or on any of:

1. a **concrete multi-repo user workload** asking "search the whole registry"
   (the same demand that would likely un-park the fleet itself);
2. registered-project count passing a point where sequential per-project
   queries visibly hurt;
3. agent/orchestrator demand for a cohort command, backed by a measured use.

## Consequences

* REMAINING-WORK §1 is now **fully parked** — the cross-repo fleet AND its
  query slice share one trigger doctrine; the verified upstream contract note
  stays for reuse.
* **No** cross-repo envelope shape, `--all`/`--cohort` surface, or
  cohort budget semantics are designed or built.
* Un-parking needs a new ADR, consistent with the fleet/delivery/RRF park
  discipline.