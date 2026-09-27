---
title: resource-limit surfacing parked indefinitely — caps stay CBM-internal
status: authoritative
date: 2026-09-25
supersedes: [docs/ROADMAP.md:56 (ops — resource-limit surfacing as the open P2 slice), docs/REMAINING-WORK.md §2 (resource-limit surfacing as an open P2 sub-item)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park resource-limit surfacing

## Context

CBM config keys `index_max_files` / `index_max_source_mb` (default `off`)
cap a whole-project index; exceeding one fails the complete index attempt and
preserves the serving DB (`fail-whole-preserve-serving`, INDEXING.md:51). A
separate always-on rule caps a single source file at 512MiB. tk exposes none of
this: `status --json`, CLI search envelopes, and the MCP mirror carry no limit
signal.

Verified upstream (`src/mcp/mcp.c`): **query tools never surface caps or
degradation.** `index_max_*` appears nowhere in MCP server code; the only
envelope carrying limit *values* (`observed`/`limit`/`unit`/`resource`/
`retryable`/`serving_index_preserved`) is the `index_repository` **error**
path (`code=resource_limit_exceeded`). Read outputs report only per-request
conditions (`truncation_reason`, `max_output_bytes`, `engine_limit`,
`scan_saturated`) and coverage best-effort signals — never configured index
caps.

Why parking is the right call *today*:

1. **Defaults are `off`.** The common configuration has no cap, so there is
   nothing to surface; the proposed output would be constant `off/off` noise
   plus a fresh `cbm config get` spawn class (`config` subcommand — not
   `cli <tool>` — needs a new `RunConfig` primitive in the single-spawn
   wrapper).
2. **The 512MiB per-file rule is impractical** for real source files — never a
   live constraint worth surfacing.
3. **Coverage already explains thinness.** When a user *does* set caps,
   `check_index_coverage` reports `not_indexed`/`skipped` counts; B's only
   marginal gain is attributing thinness to a cap — a diagnostics nicety, not
   a scout-correctness gap.
4. **No evidence-backed demand.** No report exists of a capped repo producing
   an absence claim that `check_index_coverage` failed to explain.

## Decision

Park resource-limit surfacing **indefinitely** — no `config get` spawn, no new
`status --json index_limits`, no gap-fragment or MCP mirror. The upstream
contract is recorded here so a re-open is mechanical, not a re-discovery.
Revisit via a new ADR when any of:

1. a real report shows a **capped repo causing a false-absence claim** that
   `check_index_coverage` could not explain;
2. **index caps become non-default** in the default/auto path upstream;
3. **systematic cap use at scale** arrives (evidence of users configuring
   limits for RAM/latency budget stories), giving the surfaced state real
   audience;
4. tk's own surfaces grow in-flight status/pagination such that the index
   generation and its cap state become **observable together for free**.

Also parked with the same doctrine: the passive `resource_limit_exceeded`
envelope pass-through on `tk index` error rendering (revisit only with trigger 1).

## Consequences

* Caps stay **CBM-internal**; tk adds no new output surface and no behaviour
  change. The single-spawn wrapper stays `cli`-prefix-only.
* `docs/REMAINING-WORK.md` §2 is now **fully parked** (trajectory +
  resource surfacing + delivery). ROADMAP ops line records all three as parked.
* The verified upstream contract stands (for reuse when/if un-parked): query
  tools surface no caps; only the `index_repository` error envelope carries
  limit values; `check_index_coverage`'s `not_indexed`/`skipped` counts are the
  cap-visible signal today.
* Un-parking needs a new ADR listing the evidence, matching the fleet/delivery/
  rrf park doctrine.