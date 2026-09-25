---
title: Scout coverage-before-absence shipped — no silent absence in any profile
status: authoritative
date: 2026-09-25
supersedes: [docs/ROADMAP.md:33 ("left open: coverage-before-absence enforcement in scout"), docs/REMAINING-WORK.md §3]
superseded-by: docs/DECISIONS/2026-09-26-trace-verb-kg-trace-alias.md (the two-tool absence enumeration — `trace_path` joins `isAbsenceTool`; and the "carries no evidence" test, which matched only prose markers and so never fired against CBM's counter output — enforcement point, byte format, and hard-error-on-probe-failure stand unchanged)
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — coverage-before-absence reaches the MCP search tools

## Context

The mvp2 envelope ADR
(`docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md:48-52`)
decided **coverage-before-absence enforcement**: absence claims must be
proven against whole-project coverage, never silent. The CLI enforced it —
`find`/`grep` annotate empty results via `annotateAbsence`
(`internal/cli/query.go:118,202`) using a `check_index_coverage` probe
(`internal/cli/validate.go:26`), and a failed probe on empty results is a
hard error. `validate` (MCP analysis profile) already annotated via
`callValidate` (`internal/mcp/server.go`).

Hole: the generic MCP dispatch served `search_graph`/`search_code` **empty
results bare** — the scout profile (and any profile using those tools) could
claim absence without any coverage proof. Tracked as REMAINING-WORK §3 /
ROADMAP:33 "left open".

## Decision

Enforce at the **tool level** (all profiles, not scout-only): after a
successful `search_graph` or `search_code` call, if the output carries no
evidence (`cbmexec.LooksEmpty`), probe whole-project coverage
(`check_index_coverage`, `scopes=["."]`) and append the verdict, byte-
consistent with the CLI:

* clean → `(coverage: clean — …)` — absence verified;
* gap → `(coverage: GAP — …; absence unverified)`;
* **probe failure → hard error envelope** (`-32000`, `absence unverified`) —
  never silent absence, CLI doctrine mirrored (`internal/mcp/server.go`,
  `annotateAbsence` helper shared with `callValidate`).

No new MCP tools; profile tool counts stay 11/14/3/22. The probe only fires
on empty results (a hit is one call; a miss is two). Tools without search
semantics (`list_projects`, `check_index_coverage`, `index_status`, …) never
annotate. `source_search` (zoekt library) is untouched — it has its own
staleness contract (zoekt-staleness ADR).

## Consequences

* Absence can no longer be claimed from the MCP search tools without a
  coverage verdict — in scout, analysis, or minimal.
* One extra CBM call on miss (bounded, same as the CLI path).
* A coverage probe failure now errors an empty `search_graph`/`search_code`
  where it previously returned bare empty — a strictness change agents must
  treat as "absence unverified", matching the CLI behavior since MVP2.

Files: decision implemented in `internal/mcp/server.go` (+ unit tests +
e2e `mcp-scout-absence-annotated`); docs edited: REMAINING-WORK §3 → shipped.