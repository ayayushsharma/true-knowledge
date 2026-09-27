---
title: Cross-repo fleet parked indefinitely — trigger-based revisit, not a promise
status: authoritative
date: 2026-09-25
supersedes: [docs/ROADMAP.md MVP2 cross-repo line + done-when (fleet in MVP2 scope), docs/REMAINING-WORK.md §1 (fleet as P1, "needs a fleet ADR")]
superseded-by: docs/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md (fleet-cohort queries slice, previously "not parked", now parked)
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park the cross-repo fleet

## Context

The cross-repo contract ADR (`docs/DECISIONS/2026-09-25-cross-repo-contract-verified.md`)
verified what upstream `cross-repo-intelligence` can and cannot do:

1. it is a per-**source-project** pass producing `CROSS_*` **protocol edges**
   only (HTTP/async/channel/gRPC/GraphQL/tRPC), needing **N runs** to link an
   N-repo clique, each after fresh bases;
2. **generic** multi-repo function-call graphs are upstream-blocked (#56, #398)
   — building them in tk would mean reimplementing parsing/graph, which the
   thin-client boundary forbids;
3. matching is extractor-dependent and flaky on real stacks where
   `Route.path` stays empty (FastAPI #678, Go Fiber/gRPC #686), while the
   upstream edge classes are still expanding (#292 protocol-aware, #612 runtime
   traces, #652 NATS, #440 Maven).

ROADMAP has since MVP1-ish held fleet as a promised MVP2 item ("P1, parked,
needs ADR") — implying it would eventually be built. That framing is
reconsidered here.

## Decision

**Park the cross-repo fleet indefinitely, with an explicit trigger.**

What is parked:

* tk fleet orchestration for `CROSS_*` edges: cohort model (registry vs
  explicit `--targets`), the N-source link pass, the generation record
  (cohort SHA-set → staleness), `--cross` flags, and `CROSS_*` surfacing in
  `arch`/`impact`/`explain` envelopes.

Rationale:

* The high-value shape of "fleet" (who calls this function across any repos)
  is upstream-owned and blocked; it cannot be thin-delegated. What tk *can*
  build today (protocol-edge linking) serves a narrow class (multi-repo
  protocol-coupled fleets), is flaky where the extractor leaves `Route.path`
  empty, and chases a moving target while upstream expands edge classes.
* Cost is real: cohort bookkeeping, staleness record, fixture spike, e2e
  maintenance, and shared-store coordination per fleet-wide link run — for
  little near-term payoff.

**Trigger to revisit (un-park):** any of

* upstream ships a stable generic cross-repo edge class (workspaces /
  cross-crate, #56/#398) usable by thin clients without reimplementation;
* a concrete user workload demonstrates fleet-wide protocol-edge need (a
  demand signal, e.g. a multi-service polyglot repo set in real use);
* a theoretical cost collapse makes the protocol-edge pass effectively free.

The trigger is a revisit condition, **not** a commitment. Un-parking requires
a new ADR.

**Not parked (separable):**

* fleet-cohort **queries** — running the existing `find`/`grep`/`explain`
  across the registered cohort with coverage-absence annotation, using no
  CBM cross-repo primitive at all. This is the user-visible "search the whole
  registry" value and stays an open, unprioritized candidate item.
* generic cross-repo call graphs stay out of tk scope **permanently**
  (CBM-BOUNDARY). They live or die upstream.

## Consequences

* ROADMAP MVP2 done-when drops the "2-fixture fleet links `CROSS_HTTP_CALLS`"
  clause; the cross-repo bullet becomes parked-indefinitely with this ADR's
  trigger.
* REMAINING-WORK §1 is demoted from "P1, needs ADR" to a parked item with a
  trigger line and the cohort-query slice noted as separate.
* No code changes; tk continues to call CBM exactly as today.