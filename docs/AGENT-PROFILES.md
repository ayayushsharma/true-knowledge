---
title: Agent profiles — 27B default
status: authoritative
date: 2026-09-23
supersedes: [compatible-implementation-spec.md §15]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# AGENT-PROFILES — tuned for 27B (not 1B-8B)

Target: 27B-class agent (32k-128k context, real tool-calling). Sub-8B models are fast filters only, never finalizers (see bottom).

## Profiles

| Profile | Tools (tk mcp) | Budgets | Use |
|---|---|---|---|
| `scout` (default for 27B) | `list_projects, check_index_coverage, index_status, search_graph, trace_path, search_code, source_search, get_file_outline, detect_changes, get_architecture, get_code_snippet` (11) | `default 6000 chars, arch 2200, toc 700`; whole-record truncate + `...truncated` + `cursor/has_more` | autonomous dev |
| `analysis` | scout + `query_graph` (Cypher) + `manage_adr` passthrough | same budgets | deep/debug, explicit opt-in |
| `minimal` | `check_index_coverage, search_graph, get_code_snippet` (3) | `default 2000` | <8B filters, IDE inline |

`index_repository` (writes) is gated behind explicit user approval in all profiles per CBM SKILL.md.

## 27B rules

* Compound `explain` (def + snippet + callers + callees) in one call — saves the `2.3 vs 4.8` tool-call gap from the CBM paper.
* `file_outline` over full `read` for orientation (`70-98%` token win).
* `check_index_coverage` before absence claims ("doesn't exist", "no callers", "dead code"). Advisory `validate` otherwise — don't hard-fail every cite.
* Fail-open: CBM down → `tk install` hint, agent continues. Loopback-only transport.
* No `query_graph` by default — Cypher is where 27B wastes calls; promote to `analysis` only.

## <8B rule

Minimal profile, no writes, no `cross-repo`, no dead-code/exhaustive claims. Provisional discovery only; 27B re-verifies (`coverage + snippet` handoff).
