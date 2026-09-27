---
id: 04-mcp
title: MCP — profiles, tool surface, result shape, absence rules
status: authoritative
date: 2026-09-27
supersedes: [compatible-implementation-spec.md §15, AGENT_DOCS/history/AGENT-PROFILES.md, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp3-memory-layer.md, AGENT_DOCS/history/DECISIONS/2026-09-24-dynamic-mcp-profile-env.md, AGENT_DOCS/history/DECISIONS/2026-09-24-mcp-inputschema-spec.md, AGENT_DOCS/history/DECISIONS/2026-09-25-ledger-append-only-history-prune.md, AGENT_DOCS/history/DECISIONS/2026-09-25-scout-coverage-before-absence.md, AGENT_DOCS/history/DECISIONS/2026-09-26-trace-verb-kg-trace-alias.md, AGENT_DOCS/history/DECISIONS/2026-09-26-structured-cbm-payloads.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

# 04-MCP — the stdio proxy

`tk mcp` is a proxy, not a server: it forwards to CBM tools one call at a time
and never launches CBM's MCP server. Profile filtering is tk-side.

## Profiles

Target model is a 27B-class agent (32k–128k context, real tool calling). Smaller
models are fast filters, never finalizers.

| Profile | Tools | Budgets | Use |
|---|---|---|---|
| `scout` (default) | 11: `list_projects`, `check_index_coverage`, `index_status`, `search_graph`, `trace_path`, `search_code`, `source_search`, `get_file_outline`, `detect_changes`, `get_architecture`, `get_code_snippet` | `default 6000`, `arch 2200`, `toc 700` | autonomous dev |
| `analysis` | `scout` + `query_graph` (Cypher) + `validate` + `manage_adr` passthrough = 14 | same | deep debug, explicit opt-in |
| `minimal` | 3: `check_index_coverage`, `search_graph`, `get_code_snippet` | `default 2000` | sub-8B filters, IDE inline |
| `memory` | `scout` (11) + 11 tk-owned memory tools = 22 | + `ledger 1500` | agents that persist knowledge; runs with CBM down |

The 11 memory tools: `mem_save`, `mem_recall`, `mem_review`, `note_save`,
`note_search`, `note_toc`, `note_reindex`, `note_review`, `ledger_update`,
`ledger_get`, `ledger_history`. `ledger_update` takes a `key` and appends.
`ledger_prune` does not exist as a tool.

`index_repository` is gated behind explicit user approval in every profile.

## Choosing a profile at runtime

The command never changes. The surface is picked at server start:

```text
--tool-profile flag  >  TK_MCP_PROFILE env  >  config mcp.profile  >  scout
```

An invalid value at any level fails the server loudly with the valid list. It
never falls back. Clients set the profile per model and per project through
their own MCP server `env`, so one `tk mcp` serves every case:

```jsonc
{ "mcpServers": { "tk": { "command": "tk", "args": ["mcp"],
  "env": { "TK_MCP_PROFILE": "memory" } } } }
```

`tk mcp-install --client … --tool-profile …` renders exactly that; `args` stays
`["mcp"]`, and `scout` emits no env at all because it is the default. There is no
cwd-to-profile detection.

Matching: a 27B dev agent on a busy repo → `memory`; a stateless one-shot model →
`scout`; a human deep dive → `analysis`; an inline sub-8B filter → `minimal`.

## Result shape

Read tools answer with the engine's payload twice. Prefer the structured half.

```json
{"result": {"content": [{"type": "text", "text": "{…compact JSON…}"}],
            "structuredContent": {…}}}
```

* `structuredContent` goes only to a client that negotiated `2025-06-18` or
  newer. `initialize` echoes a revision tk knows and offers `2025-06-18`
  otherwise; an uninitialized client gets the oldest dialect. An unknown version
  is still a session, not a refusal.
* The text block is a string encoding of the same payload, budgeted once, on the
  payload, so the two encodings cannot disagree about what was dropped.
* `manage_adr`, `source_search`, and the 11 memory tools stay content-only. A
  write is reported in prose; the other two have no engine payload to structure.
* No `outputSchema` in `tools/list`, deliberately: the spec makes conformance
  mandatory only if declared, and a per-tool tk schema would drift permanently
  against CBM payloads.
* `tools/list` advertises every tool with a spec-compliant `inputSchema`:
  `type: object`, `properties`, and truthful `required`. Closed sets are enums —
  `direction` = `inbound|outbound|both`, `scope` = `project|global`, review
  `action` = `list|approve|reject`. `manage_adr` keeps a loose passthrough
  schema rather than inventing a subfield contract.
* Hidden tools are enforced, not just hidden: a tool outside the profile returns
  JSON-RPC `-32601 unknown tool` even when it is implemented, and the gate runs
  before any dispatch, including the `source_search` special case.
* Notifications get no reply. `notifications/initialized` gets no frame; other
  `notifications/*` get no null-id error. `notifications/cancelled` is honored
  structurally: every request runs on a context derived from the server's, so the
  CBM spawn dies with it. There is no per-request registry.

## Absence before belief

An empty result is absence only when an engine counter says zero. `nil` or
unknown is never "empty".

* `search_graph`, `search_code`, and `trace_path` annotate every empty result
  with `check_index_coverage`: `(coverage: clean — …)` when the index covers the
  project, `(coverage: GAP — …; absence unverified)` when it does not.
* A failed probe on an empty result is a hard error: `-32000` over MCP,
  `absence unverified`. Never a silent absence.
* The evidence test reads the engine's counters, not prose markers. At least one
  counter must be present before emptiness is claimed, and a single non-zero
  counter disqualifies it: `direction=both` with 2 callers and 0 callees is a
  hit. Metadata keys (`result_offset`, `elapsed_ms`, `has_more`, `truncated`,
  `*_relation`) are never evidence.
* An unresolvable name is an error, not an empty result. The engine returns
  `isError: true` with a hint, and tk renders the hint.
* `list_projects`, `index_status`, `check_index_coverage`, `detect_changes`, and
  `source_search` never annotate. There is no "nothing exists" claim to prove.
* `source_search` carries its own freshness contract instead — see
  `05-INDEXING.md`.
* A hit costs 1 call, a miss 2. That is the price of a claim you can defend.

## Budgets

`--budget` and `budgets.default_chars` are character bounds. Character-slicing
JSON produces invalid JSON, which is worse than no bound, so `BudgetResult`
walks the payload, drops whole rows from the end, and marks what it dropped:

```json
{"data": { …, "budget_truncated": true, "rows_dropped": 12 }}
```

`budget_truncated`, never `truncated` — the engine's own `truncated` means its
page limit fired, and overwriting it is forbidden. Scalars and evidence counters
are never dropped. If dropping every droppable row still does not fit, tk
returns the full payload with `budget_exceeded: true` rather than cut. The
output is always valid JSON.

Droppable containers are the array-of-arrays `rows`, and the flat object arrays
`groups`, `projects`, `scopes`, `impacted`. Arrays of scalars are never touched,
because `query_graph` pairs `columns` with positional `rows` and dropping a
column re-indexes everything after it. Order of descent is deepest-first and
key-sorted, so the result is deterministic and testable.

On the text face the equivalent is a whole-record cut plus a `...truncated`
marker. `has_more`, `next_offset`, and `truncation_reason` are the engine's own
paging fields, passed through; tk issues no cursors (`08-BACKLOG.md`).

## The `--json` envelope on CBM commands

```json
{"ok": true, "project": "demo", "route": "search_graph",
 "data": {…engine payload, byte for byte…},
 "absence": {"empty": false},
 "fresh": true, "head": "abc123", "current": true,
 "zoekt_head": "abc123", "zoekt_fresh": true}
```

`text` is dropped from `--json` on CBM commands. tk-owned commands keep `text`
and gain nothing: `status`, `config`, `source-search`, `mem`, `note`, `ledger`.

Multi-call commands compose their own `data`: `explain` returns
`{"definition": {…}, "traversal": {…}}`; `validate` returns
`{"valid": bool, "match": {…}|null, "near_miss": {…}|null, "coverage": {…}}`.

Remember: these shapes carry no compatibility guarantee. See `00-INDEX.md`.

## 27B operating rules

* Compound `explain` in one call instead of a find-then-fetch pair.
* Reach for `trace` when one direction is all you need: `inbound` answers "who
  calls X", `outbound --depth N` answers "what breaks if X changes".
* `get_file_outline` over reading a whole file for orientation.
* Check coverage before any negative claim. An empty result from
  `search_graph`, `search_code`, or `trace_path` already carries its verdict —
  read it before believing the absence.
* An empty trace is usually a name-resolution miss, so resolve the exact name
  with `search_graph` first.
* Fail-open: CBM down means a `tk install` hint, not a blocked agent.
* Memory hygiene: keep facts topical, keep ledgers lean, and only `note_save`
  durable truths. Capture is cheap, approval is the gate, and secrets are masked
  either way.
* No `query_graph` by default. Cypher is where a 27B wastes calls; promote to
  `analysis` when it is warranted.
