---
title: MVP2 non-fleet scope — envelope, profiles, facade, validate, coverage
status: authoritative
date: 2026-09-23
supersedes: []
superseded-by: docs/DECISIONS/2026-09-25-cross-repo-contract-verified.md (cross-repo framing: per-source `cross-repo-intelligence` pass, not a one-call fleet link)
---

# ADR — MVP2 without fleet (envelope-first wrapper, profiles, facade, validate)

## Context

Upstream CBM source proves the link pass (`cross-repo-intelligence`) needs no
tk-driven two-pass loop: one call with `mode=cross-repo-intelligence` plus
`target_projects=["*"]` links the fleet, and `get_architecture` reports
`cross_repo_links` for free. Fleet orchestration (link timing, generation
record, `--cross/--targets`) is parked as future work — it has zero coupling
with the rest of MVP2 (`grep fleet|CROSS_|cross-repo|target_projects` in
`internal/` returns nothing). The CBM `cli --json` outer flag returns full MCP
envelopes while older binaries emit compact tree text, so tk must parse
envelopes with a tree fallback. Native CBM profiles (`scout` 8 tools,
`analysis` 13 tools behind `--tool-profile`) map 1:1 onto tk's passthrough
plan with no reimplementation.

## Decision

* **Envelope-first wrapper:** `internal/cbmexec` gains `RunJSON`
  (`cli --json <tool> --args-file`, envelope unwrapped to display text) with
  automatic fallback to legacy `Run` on flag failure, bad exit, or
  non-envelope output. `UnwrapEnvelope` handles content-array results, plain
  string results, and error envelopes; anything else stays raw tree text.
  Read paths (`find/explain/grep/outline/impact/arch/query`, new `validate`)
  use it; writes (`index/sync`) stay on legacy `Run`.
* **Profiles as tk-side filters:** `tk mcp --tool-profile scout|analysis|minimal`
  (default `scout`). `scout` keeps the 11 tools, `analysis` adds
  `query_graph`, `manage_adr` passthrough, and `validate` (14),
  `minimal` keeps `check_index_coverage, search_graph, get_code_snippet` (3).
  The flag mirrors CBM's native `--tool-profile` naming; filtering stays in tk
  because tk proxies tool-by-tool rather than launching CBM's MCP server.
* **Facade aliases, zero behavior change:** `kg_find`, `kg_explain`, `kg_grep`
  are Cobra aliases over `find`, `explain`, `grep` (backward compatible,
  completion sees both).
* **`tk validate` command:** symbol existence via `search_graph name_pattern`
  (case-insensitive hit means valid), camel/snake token search for near-miss
  candidates, mandatory `check_index_coverage` annotation. Exit 0 with a
  verdict (`valid` bool in `--json`); exposed in MCP as `validate`
  (analysis profile only).
* **Coverage-before-absence enforcement:** empty-looking results from
  `find`/`grep`/`validate` trigger a `check_index_coverage` call. Clean
  coverage annotates verified absence; a coverage gap warns absence
  unverified; a failed coverage call on empty results is a hard error
  (fail loudly, never silent absence).
* **Fleet parked, not dropped:** link pass, generation record, `--cross`,
  `--targets`, fleet sync rule, and the V1 probe truth (explicit names vs
  `"*"`, `repo_path` semantics, `/tmp/xa+xb` fixtures) wait for a dedicated
  fleet ADR. Nothing in this scope reads fleet state because nothing writes it.

## Consequences

* One extra spawn attempt per read only when the envelope form fails
  (fake-backend e2e exercises the fallback); the common path costs one call.
* MCP `tools/list` counts become profile-dependent (11/14/3); e2e asserts
  all three.
* ROADMAP cross-repo line stays promised-but-parked with a pointer here until
  the fleet ADR lands.
