---
title: trajectory.ndjson parked indefinitely — ops debt, split from the evals decision
status: authoritative
date: 2026-09-25
supersedes: [docs/REMAINING-WORK.md §2 (trajectory.ndjson as an open P2 sub-item), docs/ROADMAP.md Ops line (diagnostics `trajectory.ndjson`)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park the trajectory stream

## Context

REMAINING-WORK §2 carried `trajectory.ndjson` as an open P2 ops item: a
session/state-transition stream (register→index→query→re-index with
head/fingerprint deltas) to sit beside `logs/tk.log`. It is deliberately OUT
of the evals harness (`docs/DECISIONS/2026-09-24-evals-harness.md:47`) —
pure ops/debug, no benchmark input.

Why parking is right today:

1. **No user or agent value.** tk.log already records every invocation
   (`internal/trace/trace.go`); trajectory would aggregate *project lifecycle*
   history that today exists nowhere (tk.json keeps only the last
   head/fingerprint), but diagnosing "was the index covering at time T?"
   across sessions is a human-ops concern with no current consumer.
2. **Phase-sized, not a slice.** It needs a schema ADR, a writer + reuse
   decision, and record points at four write sites — real surface for a
   non-feedback path.
3. **It doesn't decay.** Nothing invalidates the idea; the design cost stays
   flat, so deferring costs nothing.

## Decision

Park `trajectory.ndjson` **indefinitely**. The schema direction is recorded
(see Consequences) so the re-open is mechanical, not a re-design. Revisit via
a new ADR when any of:

1. a **support/forensics workflow** actually needs cross-session lifecycle
   ("when did this project's index last cover the tree?");
2. cache/scale **post-mortems** recur and tk.log's per-invocation records
   prove insufficient;
3. the evals harness effort lands and its environment wants a project-state
   stream (stay consistent with evals ADR:47: trajectory is NOT an eval input).

## Consequences

* `state/trajectory.ndjson` is not created; the trace log stays the one
  session artifact (`logs/tk.log`, 0600, 10MB×2 rotation).
* Recorded future shape (unbuilt, deferred): one JSONL record per
  **transition**, not invocation — `{ts, project, op, from_head→to_head,
  fp_old→fp_new, fresh?}` written at register / index / sync-noop / query;
  same safety contract as trace (best-effort, redaction, rotation).
* REMAINING-WORK §2 shrinks to the resource-limit surfacing sub-item plus the
  delivery park.