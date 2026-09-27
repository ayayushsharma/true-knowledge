---
title: Evals harness design — record + rule-based verdicts, frozen fixture (drafted, not yet built)
status: authoritative
date: 2026-09-24
supersedes: [docs/ROADMAP.md §MVP4-Evals (bare bullet → detailed design)]
superseded-by: null
---

# ADR — MVP4 evals harness (design locked, implementation deferred)

## Context

MVP4's "Evals" bullet (frozen-SHA `PASS/PARTIAL/FAIL` + tokens/tool-calls for
27B + <8B) is the evidence that gates every agent-correctness claim, but there
is no scaffolding today: the only test is `tests/e2e_mvp1.sh`, which builds a
throwaway one-function fixture at runtime. A real harness needs a
pin-able project, scripted agent journeys, and deterministic verdicts that run
in CI without a model.

## Decision

### Fixture: committed, frozen by git SHA

* New `tests/evals/fixture/`, committed in-repo: a small fictional Go module
  with a real call graph — `ComputeHash` called from ≥4 sites, `Retry` calling
  `Backoff`, a deprecated API with one live call-site (for `impact`),
  several functions per file (for `outline`), near-miss symbol names (for
  absence/near-miss claims), plus `go.mod` + README.
* The git SHA of the fixture **is** the frozen pin. A checked-in
  `tests/evals/fixture.sha` enforces drift: any mismatch is a hard FAIL with a
  hint (mirror the docs-ADR discipline: never rewrite, stamp superseded).
* Rationale: CI-safe, no network/2GB clones (`tensorflow@9edd58f895c` proven
  viable for one-off smokes but not CI).

### Runner: `tests/evals/run.sh` (tests-only, no product surface)

* Mirror `tests/e2e_mvp1.sh` conventions: fake-CBM default (CI-safe), optional
  `TK_LIVE=1` + `TK_CBM_BIN`, isolated `TK_HOME`, `set -eu`, shared helpers.
* Phases: init → register fixture → index → run journeys → emit
  `tests/evals/report.json`.
* Per invocation, capture the tk.log record: `argv`, `exit`, `output.chars`,
  `dur_ms`, `events[]` (backend/op/ms/ok). Tool-calls = invocation count +
  event breakdown; **est_tokens = `chars/4` proxy** (no tokenizer ships in tk;
  label as proxy, never a real token count).
* **No `tk eval` subcommand.** Evals are a dev/human evidence tool — CBM-BOUNDARY
  ("if Unix already does it, tk doesn't reimplement") keeps them out of the
  shipped CLI. `trajectory.ndjson` (MVP4 ops) stays a separate diagnostics
  item and is NOT part of this harness.

### Verdicts: rule-based, no LLM (record-only first)

* PASS — exit 0 + required output substring + probe-call ceiling honored.
* PARTIAL — correct but coverage-dependent, or probe ceiling exceeded.
* FAIL — non-zero exit, expected artifact absent, or output truncated by budget.
* Metrics stay deterministic; the transcript (tk.log excerpts in `report.json`)
  is kept so a **self-judging 27B scorer can be bolted on later as phase 2** —
  never the default mode (non-deterministic, needs a live model).

### Journeys

27B (profiles `analysis`/`memory`) —
`who-calls` (find+explain), `breakage` (impact), `orient` (source-search →
outline), `grep-route` (kg_grep), `retention` (mem save → note approve →
fresh-process recall), `secret-gate` (secret-shaped fact → review queue + zero
leak in tk.log), `semantic-recall` (embedding; **auto-skipped** when
`/api/tags` on `http://127.0.0.1:11434` is absent).
<8B — `filter` (minimal profile replay of the `orient` sub-journey; ≤3 tool
types, depth claims get PARTIAL not FAIL).

## Consequence

* `report.json` is committed with results as evidence; drift = FAIL.
* Unblocks: honest 27B-vs-<8B comparisons, budget/ceiling tuning, charger
  regressions in one file.
* Implementation deferred until the agent surface stabilizes (nothing else in
  MVP3/current work depends on it).