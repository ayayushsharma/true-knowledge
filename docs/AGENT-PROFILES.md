---
title: Agent profiles — 27B default
status: authoritative
date: 2026-09-24
supersedes: [compatible-implementation-spec.md §15, docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md (tk-side profile filter + validate), docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (memory profile), docs/DECISIONS/2026-09-24-dynamic-mcp-profile-env.md (profile via env)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# AGENT-PROFILES — tuned for 27B (not 1B-8B)

Target: 27B-class agent (32k-128k context, real tool-calling). Sub-8B models are fast filters only, never finalizers (see bottom).

## Profiles

| Profile | Tools (tk mcp) | Budgets | Use |
|---|---|---|---|
| `scout` (default for 27B) | `list_projects, check_index_coverage, index_status, search_graph, trace_path, search_code, source_search, get_file_outline, detect_changes, get_architecture, get_code_snippet` (11) | `default 6000 chars, arch 2200, toc 700`; whole-record truncate + `...truncated` + `cursor/has_more` | autonomous dev |
| `analysis` | scout + `query_graph` (Cypher) + `manage_adr` passthrough + `validate` | same budgets | deep/debug, explicit opt-in |
| `minimal` | `check_index_coverage, search_graph, get_code_snippet` (3) | `default 2000` | <8B filters, IDE inline |
| `memory` | scout (11) + `mem_save, mem_recall, mem_review, note_save, note_search, note_toc, note_reindex, note_review, ledger_update` (20) | same budgets + `notes_toc 700`, `ledger 1500` | agents that persist knowledge; runs without CBM |

`index_repository` (writes) is gated behind explicit user approval in all profiles per CBM SKILL.md.

## Profile selection — a runtime knob, not an install decision

The MCP command never changes (`tk mcp`); the tool surface is picked at server
start, in order: explicit `--tool-profile` flag > `TK_MCP_PROFILE` env >
config `mcp.profile` > `scout`. Invalid values fail loudly — never a silent
fallback. So different models, integrations, and projects each get their own
profile by setting `TK_MCP_PROFILE` in the client's MCP server `env`:

```jsonc
// opencode.json — per-project/per-model profile via env, same `tk mcp`
{ "mcpServers": { "tk": { "command": "tk", "args": ["mcp"],
  "env": { "TK_MCP_PROFILE": "memory" } } } }
```

`tk mcp-install --client ... --tool-profile <profile>` renders exactly that
(`args` stays `["mcp"]`); clients override per project by editing env. Example
matrix: a 27B dev agent on a busy repo → `memory` for knowledge continuity; a
stateless one-shot model → `scout`; human deep-dive → `analysis`; an IDE-inline
<8B filter → `minimal`.

Memory-profile tools are tk-owned and in-process: they never require the graph backend, so long-term memory keeps working when CBM is missing or down.

## 27B rules

* Compound `explain` (def + snippet + callers + callees) in one call — saves the `2.3 vs 4.8` tool-call gap from the CBM paper.
* `file_outline` over full `read` for orientation (`70-98%` token win).
* `check_index_coverage` before absence claims ("doesn't exist", "no callers", "dead code"). `tk validate` (existence + near-miss, coverage-annotated) otherwise — don't hard-fail every cite.
* Fail-open: CBM down → `tk install` hint, agent continues. Loopback-only transport.
* Memory-profile hygiene: keep facts topical (`mem_recall` is exact-topic), keep ledgers lean (they're budget-truncated, never a scratch pad), and only `note_save` durable truths — capture is cheap, approval is the gate, and secrets are masked either way.
* No `query_graph` by default — Cypher is where 27B wastes calls; promote to `analysis` only.

## <8B rule

Minimal profile, no writes, no `cross-repo`, no dead-code/exhaustive claims. Provisional discovery only; 27B re-verifies (`coverage + snippet` handoff).
