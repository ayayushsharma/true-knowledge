---
title: Standalone `tk trace` verb + `kg_trace` alias over CBM `trace_path`, absence-annotated on both surfaces
status: authoritative
date: 2026-09-26
supersedes: [docs/DECISIONS/2026-09-25-scout-coverage-before-absence.md (the two-tool absence enumeration — `isAbsenceTool` now also covers `trace_path`; and the "carries no evidence" test, which matched only prose markers and therefore never fired against the real engine's counter output — the enforcement point, byte format, and hard-error-on-probe-failure stand unchanged), docs/DECISIONS/2026-09-26-select-flag-forces-project-picker.md (the "nine project-resolving commands" enumeration — `trace` is the tenth; the per-command flag, precedence, and TTY gating stand unchanged)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — `tk trace` is the fourth facade verb, and its misses are proven

## Context

`compatible-implementation-spec.md:186` lists `tk trace <symbol>` in the
recommended command groups, and §8.4 (`:504`) plus the compat method list
(`:738`) list `kg_trace` as a legacy client RPC that "should remain
available through the CLI". Neither shipped: `docs/ROADMAP.md:29` recorded
`trace` as pending, and only `kg_find`/`kg_explain`/`kg_grep` exist as
Cobra aliases (the facade-verb ADR deliberately narrowed the legacy set to
the three that had a facade to alias).

Meanwhile `trace_path` was **not** unreachable — tk already shipped it
twice:

* the MCP catalog (`internal/mcp/server.go:165-170`, in `scout`,
  `analysis`, and `memory`) with schema
  `project` + `function_name` + `direction` + `depth 1-5`;
* `tk explain`, which hardcoded one call at `direction:"both"`,
  `depth:1` (`internal/cli/query.go:193`) and degraded inline when it
  failed.

So the gap was not capability, it was **ergonomics and honesty**:

* `tk explain` cannot tune direction or depth — the two questions the
  tool exists for ("what breaks if this changes" = `outbound`/deep;
  "is this dead code" = `inbound`/shallow) are unreachable, and asking
  them costs a second hand-rolled `tk cbm trace_path` call with raw
  argv.
* a CLI verb is required for `kg_trace` compat, and one call was
  justified on its own (no extra spawn versus today: `explain` is
  unchanged).

The correctness gap is the sharper one. An empty `trace_path` is a
**negative claim** — "nothing calls `X`" / "this calls nothing" — and the
engine's own troubleshooting guidance for a 0-result trace is that it is
usually a *name-resolution miss* (a partial or differently-qualified name),
not a real absence. On both tk surfaces that claim shipped bare:
`isAbsenceTool` (`internal/mcp/server.go:436`) whitelists only
`search_graph`/`search_code`, and no CLI path annotated it. A 27B agent
reading "0 callers" and writing "this function is unused" had no coverage
proof, which is exactly the failure mode the coverage-before-absence
enforcement was created to prevent.

## Decision

Ship **`tk trace`** as the fourth facade verb, aliased **`kg_trace`**, on
CBM's existing `trace_path`. No new backend tool, no new store, no new
protocol, no profile change.

### 1. Grammar — strict flag-only, the tenth project-resolving command

Unchanged from the flag-only forms ADR: `Args: cobra.NoArgs`,
`MarkFlagRequired("symbol")`, never a positional payload, never
positional project sniffing, `--project` required in practice but never
itself marked required.

```text
tk trace --symbol <symbol> [--project <name>] [--direction inbound|outbound|both] [--depth 1-5]
tk kg_trace --symbol <symbol> --project demo
```

| flag | default | notes |
|---|---|---|
| `--symbol` | *(required)* | payload; maps to CBM `function_name` (same payload flag name `explain`/`validate` use for a symbol) |
| `--project` | auto / picker | `projectFlagCompletion` from the registry; `requireProject` routing hint + TTY-gated picker |
| `-s, --select` | `false` | `selectFlag`; forces the picker, `--project` wins beside it (ADR-018) |
| `--direction` | `inbound` | `inbound`=callers, `outbound`=callees, `both` — the engine's own default, so tk's default never silently widens the traversal |
| `--depth` | `1` | engine range 1–5; matches the documented perf anchor (`<10ms`) and the bounded-call-set doctrine |

`--direction` and `--depth` are validated **locally, before the CBM
gate** — a bad value is a hard `[tk]` error, never a silently empty
traversal. Same doctrine as `impact`'s `--direction` (allowlist before
`needCBM`) and `grep --regex` (compile before spawn).

`--limit` is deliberately absent: `trace_path` has no limit parameter, and
tk never invents engine params. Traversal size is bounded by
`--depth` (1–5) and the output by the standard char budget.

### 2. One spawn, the same wrapper, the same budget

`cbmCallJSON` (envelope-first `cbmexec.RunJSON`, timing recorded into
`tk.log` as `op:"trace_path"`), then `cbmexec.Truncate(out,
ctx.budget(""))` and `ctx.outFresh` with `{symbol, project, direction,
depth}` plus the merged freshness envelope. A miss costs a second spawn
(`check_index_coverage`, `scopes:["."]`) — bounded, identical to `find`
and `grep`.

`tk explain` is refactored to call the same `tracePath` helper at
`direction:"both", depth:1`, so the two surfaces cannot drift in payload
shape. Behavior is byte-identical: `explain`'s snippet call stays fatal
and its trace call stays a degraded inline `(trace unavailable: …)`.

The CLI verb is profile-independent, as every other CLI verb is — it
works even though `minimal` hides `trace_path` from MCP.

### 3. No silent absence on either surface

`trace_path` joins `isAbsenceTool`, so the same rule the search tools
already follow now covers traversal:

* clean coverage → `(coverage: clean — …)` — absence verified;
* gap → `(coverage: GAP — …; absence unverified)`;
* **probe failure → hard error, never a bare empty result** (CLI: `fail`,
  MCP: `-32000`) — the enforcement point, byte format, and
  hard-error rule are exactly those already shipped; only the
  enumeration grows from two tools to three.

This is the ADR it supersedes that changes: coverage-before-absence was
decided for "the search tools" and enumerated `search_graph` /
`search_code`. A traversal is a search over the call graph with the same
name-resolution failure mode, so it earns the same proof. Nothing else
from that ADR moves — inventory tools (`list_projects`, `index_status`,
`check_index_coverage`) still never annotate, and `source_search` keeps
its own zoekt staleness contract.

`detect_changes` (`tk impact`) still does **not** annotate: its output is
a diff mapping, not a "nothing exists" claim.

### 4. Two prerequisites, found by running the real engine

The decision above is unimplementable as written against the binary it
delegates to, so both were verified by installing CBM 0.11.0 through
`tk install` and calling the engine directly. Two facts on the wire
decide everything:

* an **empty result is counters, not prose**. A zero-caller trace is
  `callers_total: 0` / `callers_total_relation: eq` /
  `callers: 0  (cols: qn hop)`; a search miss is `results: 0` /
  `total: 0` / `returned: 0`. Neither contains any phrase in
  `LooksEmpty`'s marker list.
* an **unresolvable name is a tool error**, not an empty result:
  `isError:true` with `{"error":"function not found","hint":"Use
  search_graph(name_pattern=...) …"}` and a nonzero exit. Loud already —
  but the remediation was being flattened into a raw JSON blob.

Both are fixed in the one wrapper, because both are prerequisites for the
rule above to hold:

1. **`cbmexec.LooksEmpty` recognizes zero counters.** Output is
   evidence-free when every evidence counter the engine printed is zero,
   with two guards: at least one counter must be present (unstructured
   text is never guessed at) and a single non-zero counter disqualifies
   emptiness, so `direction=both` on a function with 2 callers and 0
   callees stays a hit. Pagination cursors (`result_offset`), timings
   (`elapsed_ms`), flags (`has_more`, `truncated`) and relation labels
   (`…_relation`) are excluded by name — they are metadata, never a
   statement about how much was found.

   This also **repairs the enforcement it extends.** Marker-only matching
   meant the coverage-before-absence ADR was inert against the real
   engine: no genuine `search_graph` or `search_code` absence was ever
   annotated, and the fake in `tests/e2e_mvp1.sh` hid it by inventing
   `no results (fake miss)`. The fake now mirrors the real counter shape,
   and the e2e guards `find`/`grep` misses as well as `trace`.
2. **`cbmexec` reads the envelope the engine actually emits and keeps the
   hint.** CBM 0.11.0's `cli --json` returns a bare MCP result object
   (`content` + `isError`), not the spec's `{"result":{…}}` wrapper, so
   every read silently fell through to the legacy re-spawn; both shapes
   are now unwrapped. `isError:true` surfaces as a tool error, and the
   engine's `hint` is rendered (`cbm trace_path failed: function not
   found (hint: …)`) instead of raw JSON. Verified against live CBM, not
   assumed.

Neither change alters a verdict, a message format, or a profile count;
they make the already-specified rules actually fire.

### 5. `kg_trace` compat

A Cobra `Aliases` entry on `trace`, exactly like `kg_find`/`kg_explain`/
`kg_grep` — zero behavior change, no second command, no second spawn. The
spec's `kg_impact`/`kg_search`/`kg_arch`/`kg_query` remain unshipped
facade-free legacy names; `trace` is the only one of that set that
finally got a facade to alias, so it is the only one added here.

## Consequences

* `tk trace --symbol X --project demo --direction outbound --depth 3`
  replaces hand-rolled `tk cbm trace_path` argv, and "who calls X" /
  "what does X break" are both one bounded call.
* Absence claims from a trace now carry a coverage verdict on the CLI and
  in `scout`/`analysis`/`memory` — one extra CBM call on a miss, a hit
  stays one call. `find`/`grep`/MCP `search_*` absences now carry one too,
  which they did not in practice before.
* An unresolvable name is a hard error whose message keeps the engine's
  own remediation hint.
* Tool counts are **unchanged**: `scout(11) | analysis(14) | minimal(3) |
  memory(22)`. `trace_path` was already in the catalog; this ADR changes
  what happens on an empty result, not what is offered.
* `tk explain` behavior is unchanged (shared helper, same arguments).
* e2e exercises the verb, the alias, both flag validations, the
  non-TTY `--select` hard-fail, the absence annotation on `trace` +
  `find` + `grep`, and that a populated result stays unannotated.
* Verified live against CBM 0.11.0 (installed via `tk install` into an
  isolated `TK_HOME`): hit, counter-shaped miss, mixed `both` counts, and
  unresolvable name.

Files: `internal/cli/query.go` (`cmdTrace`, `tracePath`,
`traceDirection`, `traceDepth`; `cmdExplain` refactor), `internal/cli/wire.go`
(registration), `internal/mcp/server.go` (`isAbsenceTool`),
`internal/cbmexec/exec.go` (`LooksEmpty` counter rule, `UnwrapEnvelope`
bare-result + `isError` + `hint`, `diagnosis`), tests in
`internal/cli/cli_test.go` + `internal/cli/wire_test.go` +
`internal/cbmexec/exec_test.go` + `internal/mcp/server_test.go` +
`tests/e2e_mvp1.sh` (fake now mirrors the real engine shape), man pages
via `mise run docs.man`. Docs edited: `docs/ROADMAP.md`,
`docs/AGENT-PROFILES.md`, `AGENTS.md`.
