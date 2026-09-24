---
title: RRF tuning parked indefinitely — future scope, revisit on an evals signal
status: authoritative
date: 2026-09-25
supersedes: [docs/ROADMAP.md:47 ("Deferred: RRF tuning knobs beyond defaults"), docs/REMAINING-WORK.md §4 (RRF tuning as an open P3 item)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park RRF tuning (but it stays in future scope)

## Context

`note search` fuses two rankers with Reciprocal Rank Fusion (`memory/embed.go`):
BM25 (FTS5, **authoritative**) and cosine similarity against embeddings from the
external Ollama-compatible `/api/embed` endpoint. Fusion is rank-based — each
list votes `1/(k + rank)` with `k = 60` hard-coded (`embed.go:120,146`), no
per-list weights.

Why configurability looked tempting: `k` and weights are the classic levers on
fusion behavior, and REMAINING-WORK §4 carried "promote to config keys
(`ui.picker` plumbing pattern)".

Why parking is the right call *today*:

1. `k=60` is the canonical RRF default (Cornack et al.); the current fusion is
   standards-true.
2. **BM25 stays authoritative** — embeddings are an optional enhancement; any
   embed failure, timeout, or model mismatch degrades to BM25-only
   (`notes.go:329`). Retrieval cannot be harmed by the fusion defaults.
3. **No evals harness is built** to judge ranking: `docs/DECISIONS/2026-09-24-evals-harness.md`
   is design-locked only. Tuning without measurement is config-for-config's-sake.
4. No real workload has shown misordering attributable to `k=60`.

## Decision

Park `k`/weight configurability **indefinitely** — no `search.rrf_k` or fusion
weight keys today. **Future scope, explicitly:** retrieval quality (note
search fusion) is a core feature; the knobs are its natural lever the moment
evidence exists. Revisit (via a new ADR — the park is trigger-based, not
"never") when any of:

1. the evals harness is **built** and demonstrates that RRF `k=60` misorders
   results vs BM25-only on the frozen fixture class;
2. embeddings are promoted from optional enhancement to the **mandatory /
   critical path** (BM25 no longer authoritative);
3. a real workload shows a **measured regression** tied to `k=60`;
4. agent/orchestrator demand for knobs arrives **backed by a measured gain**.

## Consequences

* Fusion stays RRF `k=60`, weight-less, until a listed trigger fires — no new
  config surface, no behavior change.
* The `ledger` compaction decision is unrelated and stays **resolved by
  design** (append-only + human-only `prune` — ledger ADR); this park does not
  reopen it.
* Un-parking needs a new ADR listing the evidence, matching the fleet/delivery
  park doctrine.