---
title: Cross-repo contract verified — per-source linking pass, not a one-call fleet link
status: authoritative
date: 2026-09-25
supersedes: [docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md (§3 cross-repo framing — see also AGENTS.md), docs/REMAINING-WORK.md §1 (cross-repo primitive claims), docs/ROADMAP.md MVP2 cross-repo line (see also AGENTS.md), docs/INDEXING.md modes table (cross-repo-intelligence row — see also AGENTS.md)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — how `codebase-memory-mcp`'s cross-repo intelligence is really used

## Context

tk's docs claimed a single `cross-repo-intelligence` call with
`target_projects=["*"]` "links the fleet" (mvp2 ADR, ROADMAP MVP2 line,
REMAINING-WORK §1). Verified against upstream source
(`DeusData/codebase-memory-mcp` main @ 2026-09-24, `src/pipeline/pass_cross_repo.c/.h`)
shows that framing is wrong on its face. This ADR records the verified
contract so the fleet work (and any code) builds on upstream reality, not a
market summary. Whether tk builds the fleet at all is settled separately by
`docs/DECISIONS/2026-09-25-fleet-parked-indefinitely.md` (parked, trigger-based).

## Verified contract (from `pass_cross_repo.c`)

`cross-repo-intelligence` is an **`index_repository` mode**, not a separate
tool — `index_repository(mode="cross-repo-intelligence", repo_path,
target_projects)`, dispatched to `cbm_cross_repo_match(project, targets, count)`.

1. **Per-source-project pass.** The call matches exactly ONE source project
   (derived from `repo_path`) against every target. `target_projects=["*"]`
   only expands the *target list* to every indexed `<cache>/*.db`
   (`collect_all_projects`, skipping `_config.db` and `_cross_repo.db` and
   invalidated/non-existent names). The source repo stays a single project:
   a full N-repo clique needs **N runs — every member as source**. One run
   links only repo X to the rest of the fleet.
2. **Preconditions are hard-fail, not skip.** Source must already be indexed
   (`.db` exists) and every named target must validate + exist, else
   `result.failed = true` — "ensure target projects have fresh indexes
   first". It is a pure linking pass: skip extraction, match against
   existing DBs.
3. **Wipe-and-rebuild per source.** Each run `delete_cross_edges(src)` then
   rebuilds the source's `CROSS_*` edges. Writes are **bidirectional**:
   forward (caller → target Route) plus a reverse scan so a provider-side
   run also resurrects consumer-side edges (the `#523` "byte-identical
   call/route → 0 edges" fix). The store upserts on `(source_id, target_id,
   type)`, so repeat runs converge without duplicates.
4. **Scope is protocol-fingerprint edges, not generic symbol linking.** The
   pass matches HTTP calls to Route qualified names (`CROSS_HTTP_CALLS`,
   URL-normalized with a fuzzy `find_route_handler_fuzzy` fallback), async
   topics (`CROSS_ASYNC_CALLS`), Channel `EMITS`↔`LISTENS_ON`
   (`CROSS_CHANNEL`), and typed gRPC / GraphQL / tRPC service+operation
   pairs (`CROSS_GRPC_CALLS` / `CROSS_GRAPHQL_CALLS` / `CROSS_TRPC_CALLS`).
   It does **not** produce generic cross-repo function CALL/symbol edges
   (workspaces, cross-crate); that class remains open upstream (#56, #398).
   Measured by `http|async|channel|grpc|graphql|trpc_edges` +
   `projects_scanned`.
5. **Matcher holes are extractor-driven.** Mismatches key on `Route.path`
   (and HTTP_CALLS url targets). Where extractors leave `Route.path` empty —
   FastAPI (#678), Go Fiber / gRPC gaps (#686) — the pass returns zero
   edges. Fleet fixtures must use a stack that populates it.

## Decision

* Corrected usage rule: after every involved base is fresh, run
  `cross-repo-intelligence` **once per source project** over
  `target_projects=["*"]` (or an explicit cohort). A project's `CROSS_*`
  edges describe the last pass where it was the source; any future tk fleet
  orchestration's generation record (cohort SHA-set → per-project last-run)
  would be the tk-owned side of that contract.
* Fleet scope is restricted to protocol edges only: `CROSS_HTTP_CALLS` /
  `CROSS_ASYNC_CALLS` / `CROSS_CHANNEL` / `CROSS_GRPC_CALLS` /
  `CROSS_GRAPHQL_CALLS` / `CROSS_TRPC_CALLS`. Generic multi-repo
  "who calls this function" stays upstream-blocked and must not be in any
  fleet scope.
* Issue the stale "one call links the fleet" framing in the mvp2 ADR /
  ROADMAP / INDEXING / REMAINING-WORK (this ADR supersedes those claims).
  No code changed; whether/how fleet gets built is governed by the
  fleet-parked ADR.

## Consequences

* Any future `--cross/--targets` fleet work is an N-source orchestration
  pass with a cohort generation record and protocol-edge envelopes — not a
  single-spawn link. It is parked per the fleet-parked ADR.
* e2e "done when" for a 2-fixture fleet would verify `CROSS_HTTP_CALLS`
  edges after running the pass from **both** fixtures as source (2 spawns).