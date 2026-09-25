---
title: Structured CBM payloads — format:"json" passthrough, not text scraping
status: authoritative
date: 2026-09-26
supersedes: [compatible-implementation-spec.md §12 (envelope text-only renders), docs/CBM-BOUNDARY.md §tk owns (reads prefer RunJSON, unwrapped to text), docs/AGENT-PROFILES.md (MCP tools/call content-only result)]
superseded-by: null
---

# ADR — pass CBM's structured data through instead of re-parsing its text

## Context

`tk` renders every CBM reply as a string. `internal/cbmexec` sends
`cbm cli --json <tool> --args-file`, then `UnwrapEnvelope` keeps the first
`content[].text` string and discards everything else in the envelope
(`internal/cbmexec/exec.go`). Downstream, `Ctx.out` puts that string in the
`--json` envelope's `"text"` field (`internal/cli/root.go:44`).

The consequence is that `tk find --json` hands an agent a rendered table
wrapped in escaped newlines:

```json
{ "ok": true, "route": "search_graph",
  "text": "results: 1  (cols: qn label file lines in out)\n  demo.Level2 Function chain.go 10-10 1 1\ntotal: 1\n…" }
```

To learn that the row count was 1, that the label was `Function`, or that
`has_more` was false, the consumer has to regex a prose rendering whose
layout is CBM's private business. `tk` then regexes that same text to make
its *own* decisions (see "Live bugs" below), so CBM's layout is load-bearing
twice over.

CBM never asked for this. **All 13 read tools accept `format: "json"`, and
with it the MCP result carries a `structuredContent` object** — typed values,
`cols`/`rows` tables, machine-readable counters, and the engine's own `hint`
strings. `content[0].text` becomes that same object JSON-encoded as a string.
This was verified against CBM 0.11.0 by driving `cbm cli --json` directly;
the payload shapes are quoted in "Engine contract" below.

Two tk behaviours are already broken by the text-only design:

1. **`tk validate` reports `invalid` for symbols that exist.**
   `internal/cli/validate.go:64` asks `search_graph` for
   `name_pattern=demo.Level2` and then checks
   `strings.Contains(text, "demo.Level2")`. `name_pattern` matches *bare*
   names, so CBM answers with the project node `demo Project {} - 0 0`; the
   `Contains` check fails; the verdict is `valid: false` for a symbol that
   `trace_path` and `get_code_snippet` both resolve happily. Reproduced live
   against 0.11.0. The text is the only thing tk has, so tk guesses.
2. **MCP advertises `manage_adr` modes that CBM rejects.**
   `internal/mcp/server.go:222` publishes
   `get|update|set_sections|sections|list|delete`. CBM 0.11.0 accepts
   `outline|get|sections|set_sections|update`: `list` and `delete` hard-error
   with `invalid mode: use outline, get, sections, set_sections, or update`,
   and `outline` — the one a model is most likely to reach for first — is
   missing from the enum.

## Decision

**Read `structuredContent` when the caller wants structure; never re-model
CBM's schema, never scrape its text to manufacture structure.**

### Dual path

| caller | CBM `format` | tk renders |
|---|---|---|
| human (`tk find`, no `--json`) | `tree` (CBM default) | CBM's own text, byte-identical to today |
| machine (`--json`, `tk mcp`) | `json` | CBM's `structuredContent`, verbatim |

Two code paths, but not two renderers: **tk never re-renders a tree.** In
`format:"json"` mode CBM emits no tree text, so the human path simply asks
CBM for `tree` and prints what comes back. That is what keeps human output
free of drift — a tk table renderer would have to reimplement CBM's kv
blocks, `cols:` tables, `source: |` blocks and `qn_prefix` grouping, and
every upstream layout change would break output for everyone.

`format:"json"` is requested only on read paths. `Run` (writes) never
carries it.

### Envelope

`--json` on a CBM-backed command becomes:

```json
{ "ok": true, "project": "demo", "route": "search_graph",
  "data": { "…CBM structuredContent, verbatim…" },
  "absence": { "empty": false },
  "fresh": true, "head": "…", "current": "…",
  "zoekt_head": "…", "zoekt_fresh": true }
```

* `data` is CBM's payload **byte-for-byte**. The thin-shipper doctrine
  (`docs/CBM-BOUNDARY.md`) says tk owns paths + config + spawn + render and
  nothing else; re-typing CBM's schema inside tk would create a second
  contract to keep in sync with the first. `find`'s existing
  `fields["route"]` tells a consumer which tool shaped `data`.
* **`text` is dropped from `--json` on CBM commands.** It is the same
  information twice, and an agent pays for it in context. `tk` is pre-1.0
  with no external `--json` consumers, so this is taken as a break rather
  than a deprecation (see "Pre-1.0 interface policy").
* tk-owned commands keep `text` and gain nothing: `status`, `config`,
  `source-search` (zoekt, in-process, never CBM), and the
  `mem`/`note`/`ledger` family. The split is deliberate — those surfaces
  have no engine payload to be structured. Giving `source-search` a
  structured shape is a follow-up, not this change.
* Two commands compose more than one CBM call, so `data` is tk's own object:
  * `explain` → `{"definition": {…get_code_snippet…}, "traversal": {…trace_path…}}`.
    Today it is a `== definition/snippet ==` / `== callers+callees ==` blob
    that no consumer can split.
  * `validate` → `{"valid": bool, "match": {…}|null, "near_miss": {…}|null, "coverage": {…}}`.

### Absence gating becomes fields, not prose

`cbmexec.LooksEmpty` exists because absence claims must be proven, and it
works by reading CBM's counters out of the text. Under `format:"json"` those
counters are JSON keys, so **`LooksEmpty` cannot be reused as-is** — its
`^key: N` line regex would match nothing and every genuine absence would
ship unproven. `LooksEmptyData` ports the same doctrine to JSON: the same
`countField` allowlist applied to top-level numeric keys, a present-but-empty
`rows`/`groups` array counting as zero evidence, at least one counter
required (never guess), and any single non-zero counter disqualifying
emptiness (`callees_total:1` + `callers_total:0` is a hit, not an absence).

`coverageVerdict` gains a structured sibling returning
`{signal, generation_matches, hash_records_complete, recording_status}`. The
one-line human verdict is **derived from it**, so the two surfaces cannot
disagree — the same byte-consistency requirement the current
`annotateAbsence` pair documents.

### Budget

`--budget` / `budgets.default_chars` is a **character** bound, and character-
slicing a JSON document produces invalid JSON — worse than no bound, because
it fails at the consumer's `jq` rather than tk's. `BudgetResult` instead
walks the payload for row containers, drops from the end until the marshalled
size fits, and records what it did:

```json
"data": { …, "budget_truncated": true, "rows_dropped": 12 }
```

The marker is named `budget_truncated`, **not** `truncated`. The engine has a
field of its own by that name, meaning "my page limit fired, ask for more" —
`search_code` and `search_graph` both send it. Overwriting it would erase the
one field that tells a caller the answer is incomplete for a *different*
reason, and the two reasons need different responses: one is "tk trimmed this",
the other is "there is more, page again".

The size check is made against the bytes that will actually be returned,
markers included. Measuring the bare tree and then adding the marker overshoots
the budget by the marker's length on every single call, which is how the
first implementation of this shipped.

Scalars and evidence counters are never dropped — they are the proof an
absence claim rests on. If dropping every droppable row still does not fit,
the full payload is returned with `budget_exceeded: true` rather than cut.
Output is always valid JSON.

**What counts as a row is deliberately narrow**, because trimming the wrong
container is a correctness bug rather than a bad cut. Two shapes qualify: an
array of arrays (`rows` — the engine's own `cols` fixes the positional
mapping) and an array of objects (`groups`, `projects`, `scopes`, `impacted`).
Arrays of scalars are never touched: `query_graph` pairs `columns` with
positional `rows`, so dropping a column would silently re-index every row left
behind. A row is registered and not descended into, because a `search_code`
row carries a `matches` array of its own that is a count, not a table.
Ordering is deepest-first and key-sorted, so the same payload always trims to
the same bytes — which is what makes a golden-output test possible at all.

### MCP

`tk mcp` has no human surface: every `tools/call` is machine-facing, so it
requests `format:"json"` on every CBM tool and emits both halves:

```json
{"result": {"content": [{"type": "text", "text": "{…compact JSON…}"}],
            "structuredContent": {…}}}
```

`structuredContent` arrived in MCP **2025-06-18**; tk previously announced
`2024-11-05` unconditionally. The `initialize` handler now negotiates: it
echoes the client's requested revision when tk knows it, and answers with tk's
newest (`2025-06-18`) when it does not — an unknown version is still a
session, not a refusal.

`structuredContent` is emitted **only to a client that negotiated 2025-06-18 or
newer**. The first draft of this ADR emitted it regardless, on the reasoning
that unknown result fields are ignored by older clients. That is true and it
is the wrong call: sending a client a field keyed to a revision it never asked
for implies an answer shape it has no way to read, and the text block already
carries the same bytes. An uninitialized client is served the oldest dialect.

`content[0].text` becomes compact JSON on MCP. In `format:"json"` mode there
is no tree text, so a client that reads only the text block sees JSON instead
of the old table. Kept as a single spawn: the alternative is two spawns per
call to carry both renderings, doubling latency on every tool call to serve a
surface that has no humans. Compact (not indented) to bound the cost for those
clients, and budgeted **once**, on the payload, so the two encodings cannot
disagree about what was dropped.

**No `outputSchema` in `tools/list`.** The 2025-06-18 spec makes conformance
mandatory *if* an output schema is declared; declaring one per tool would put a
tk-side schema per read tool under permanent drift against CBM's evolving
payloads, for no consumer benefit — a client that wants the shape can read the
payload it was just handed. The spec's own requirement is a `SHOULD`. Recorded
here so the omission is a decision rather than an oversight, and because
`tk mcp` serving structured payloads without schemas is exactly the state a
future reader will mistake for an oversight.

`manage_adr` is excluded from the structured set even though the engine accepts
the format: it also writes, and a write is reported in prose — a caller needs
to read "updated" or the new revision id, not parse a payload that may or may
not exist depending on the mode.

`source_search` (zoekt) and the 11 memory tools stay content-only — no
engine payload to structure.

### Engine contract (CBM 0.11.0, captured live)

`format: "json"` is accepted by `search_graph`, `query_graph`, `trace_path`,
`get_code_snippet`, `get_file_outline`, `get_graph_schema`,
`get_architecture`, `search_code`, `list_projects`, `index_status`,
`check_index_coverage`, `detect_changes`, `manage_adr` — all `default: "tree"`.

Shapes that shape tk's code (abridged from real captures):

* `search_graph` — `qn_rule`, `cols[]`, `groups[]{qn_prefix, file, rows[][]}`,
  `total`/`returned`/`count`/`has_more`/`next_offset`/`truncated`/
  `truncation_reason`/`hint`. The semantic route swaps in
  `semantic{cols,rows}` + `semantic_total`.
* `trace_path` — `function`, `direction`, `callees`/`callers` as
  `{qn_rule, cols, groups[]}`, `*_total`, `*_total_relation`.
* `get_code_snippet` — `name`, `qualified_name`, `label`, `file_path`,
  `start_line`, `end_line`, `source_mode`, `source`, `callers`, `callees`.
* `search_code` — `cols[]`+`rows[][]` (with `matches` as real `[13]` arrays
  rather than the tree's `"13"`), `raw_matches{cols,rows}`,
  `directories{}` as a map, `total_grep_matches`, `dedup_ratio`.
* `check_index_coverage` — `signal`, `metadata{generation_matches,
  hash_records_complete, recording_status, index_mode}`,
  `scopes[]{requested_scope, total, returned, entries, status}`, `caveat`.
* `detect_changes` — `base`, `merge_base`, `changed_files[]`, `seed_symbols`,
  `impacted[]`, `impacted_modules[]`, `*_total_relation`.
* errors — `{"error": …, "hint": …}` with `isError: true`, the same
  `diagnosis()` hint reachability the text path already has.

The row containers are **irregular**, which is why `BudgetResult` is
recursive: `rows[][]` (`get_file_outline`), `groups[].rows[][]`
(`search_graph`, `trace_path`), a nested `raw_matches.rows[][]` plus a
`directories` map (`search_code`), and flat object arrays
(`list_projects`, `check_index_coverage.scopes`, `detect_changes`).

### Two defects found while verifying against the live engine

Both were found by running the real binary against a real index, and both are
fixed here. They are recorded because the design above reads as though the
lookup question was already answered, and it was not — it was answered
*incorrectly*, in two independent ways.

**1. `validate` denied symbols that exist.** It decided existence with
`strings.Contains(lower(reply), lower(sym))` over the rendered text. That is
wrong on both sides:

* the engine matches `name_pattern` against the leaf `name`, never a qualified
  name, so searching for `sweep.Level2` returns `total: 0` — the symbol is not
  in the reply to be found;
* a search renders a row for the project itself,
  `sweep Project {} - 0 0`, which *does* contain the prefix — so the
  substring test would have matched a reply that found nothing, had the
  emptiness check not short-circuited first.

Live before: `tk validate --symbol sweep.Level2 --project sweep` printed
`valid: false` for a function with two callers.

**2. The `manage_adr` schema offered modes the engine rejects.** The enum
advertised `get|update|set_sections|sections|list|delete`; the engine's own
vocabulary is `outline|get|sections|set_sections|update`. An agent that
trusted the schema and called `list` got a validation error from a tool whose
own description promised it would work.

The fix for (1) is `cbmexec.LeafName` + `cbmexec.MatchSymbol` over
`QualifiedNames` (payload) or `QualifiedNamesFromText` (tree), shared by the
CLI and the MCP `validate` tool so there is one rule rather than two. It is
exact, and a dotted symbol must match whole — asking about `a.b.c` and finding
`a.b.x` is a miss, not a near hit.

The lesson generalises past this ADR: **the tree's first column is the
qualified name, and the payload's `qn_prefix`+`name` pair reassembles to the
same string.** Both are now read structurally, because a substring test over a
rendered reply answers a different question than the one being asked.

### Degradation

An older binary that predates `format` ignores the unknown argument and
sends no `structuredContent`. `RunStructured` then returns `Data == nil` with
the legacy text and no error — **absence of `structuredContent` is the
capability probe.** The result is memoized on the `*Runner` for the process
lifetime, so a hypothetical binary that *rejects* unknown args cannot break
the second call. Nothing is ever reported as structured that is not.

### Pre-1.0 interface policy

`tk` is 0.x and pre-release. There is **no compatibility guarantee** on
`--json` envelope keys, MCP result shape, flag names, or tool-list schemas.
Breaking changes are made in favour of better interfaces and long-term
project health; consumers should pin a version. Recorded in
`docs/00-AUTHORITY.md`. This is what makes dropping `text` from the
structured envelope a decision instead of a deprecation cycle.

## Consequence

* Agents read typed data on both surfaces: no escaping, no column-order
  guessing, real arrays (`matches: [13]`) instead of `"13"`, and CBM's own
  `hint`/`caveat` as fields.
* `tk` stops depending on CBM's text layout for its own decisions. The two
  live bugs above are fixed as a consequence, not as a side quest.
* Human output is untouched: the human path is the old call verbatim — same
  argv, same engine rendering, byte-identical. It is a *separate* spawn from
  the `--json` path, because one spawn cannot produce both a table and a
  payload. The cost is one extra spawn on the surface nobody was reading.
* `tk` still owns no graph semantics. It forwards the engine's payload and
  adds exactly three things of its own — the freshness fields, the absence
  verdict, and the budget markers.
* `text` consumers of `--json` on CBM commands break. Mitigation is the
  pre-release policy, not a compatibility shim.
* The `budgets.*_chars` knobs now mean "drop whole rows" on the structured
  path rather than "cut at a character" — the same total-ordering guarantee,
  expressed in units that keep the document parseable.
* `LooksEmpty`/`LooksEmptyData` are a matched pair and must stay in step; a
  divergence is a silent loss of coverage-before-absence, not a cosmetic
  bug. Both are pinned by verbatim engine fixtures.
