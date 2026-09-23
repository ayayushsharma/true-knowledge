---
title: Roadmap — MVP1 through MVP4
status: authoritative
date: 2026-09-23
supersedes: [compatible-implementation-spec.md §20, docs/DECISIONS/2026-09-22-thin-tk-over-cbm.md (scope)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ROADMAP — thin proxy first, memory later, human polish last

Locked decisions: Go-only thin `tk` over CBM, Linux-style `true-knowledge/` dirs everywhere, no supervisor daemon, 27B default agent, Cobra standard-dynamic completion, human UX deferred, cross-repo soon.

## MVP1 — thin proxy + self-installing backends (code complete, real-binary verified)

* `init/register/index/sync/status/find/search/explain/grep/arch/query/cbm/mcp/config/migrate/install/setup/mcp-install/completion/source-search`.
* One spawn wrapper `internal/cbmexec`: `codebase-memory-mcp cli <tool> --args-file <json>` (raw-JSON argv is deprecated upstream) + env (`CBM_CACHE_DIR`, `CBM_RUNTIME_DIR`, `CBM_ALLOWED_ROOT`) + budget truncation + fail-open. Project is required — tk never sends `""`.
* `tk` installs ALL its external dependencies itself: `internal/backends` registry (CBM today) + `internal/installer` (pinned download, SHA-256 manifest verify, atomic `<cache>/bin` install, no system package managers). Zoekt is NOT a backend — it links in as a Go library (`go.mod` pin; same-language links, cross-language spawns). `tk setup` = init + install + opt-in register/client. Resolver: `TK_CBM_BIN` > sibling > cache > PATH > download.
* MCP 9-tool proxy: scout-7 + snippet + `source_search` (zoekt, hard error when absent).
* `source-search`: explicit Zoekt trigram text (no magic routing), served in-process via `internal/zoekttext` (`gitindex`/`index`/`shards`/`query` at the `go.mod` pin); shards at `<cache>/zoekt/<project>/`, `zoekt_head` tracked in `tk.json`.
* XDG `true-knowledge/{config,data,cache,state}` + `TK_*`/`TK_HOME` isolated test homes.
* No supervisor, no facts/notes/ledger, no graph code in `tk`.
* Done when: `TK_HOME=/tmp/x` smoke (`setup → register → index → sync no-op → status --json → mcp tools/list` = 9) passes with zero `~/.tk`/`Library` writes — verified against real CBM 0.11.0 + zoekt @153817f6 (spawn spike; library port per zoekt-library ADR).

## MVP2 — code-intel depth + cross-repo fleet

*Why first:* flagged focus + unblocks 27B autonomy. Still delegated to CBM, no new stores.
* `kg_*` facade: `kg_find` (regex→grep / ident→graph / NL→semantic router), `kg_explain` (def+snippet+callers+callees, one call), `kg_grep`; legacy `search/trace/arch/query` compat.
* `analysis` profile: `query_graph` (read-only, `LIMIT` + timeout guardrails), `get_file_outline`, `validate` (existence + near-miss), `detect_changes → impact` (diff → blast radius + risk).
* Cross-repo: `tk index --cross --targets "*"` = base loop then `cross-repo-intelligence` pass; `CROSS_*` edges + fleet arch summary.
* Freshness: `project+generation+head_sha` on every response, coverage-before-absence in `scout`, stale-cursor errors.
* Done when: 27B answers `who calls X / what breaks if Y changes / outline Z` in ≤3 calls; 2-fixture fleet links `CROSS_HTTP_CALLS`.

## MVP3 — memory layer (tk-owned; CBM has no equivalent)

*Why after:* three new tk-owned stores with review/privacy risk; kept off MVP2's path.
* `mem_*` facts: `(scope,project,topic)` UPSERT, global sentinel cross-project, provenance+timestamp, secret-detect → review queue (never silent store).
* `note_*` notes: `notes/<project>/*.md` source-of-truth + FTS + optional local embeddings (BM25 fallback), title-only `toc` injection, `reindex`, capture→review→approve.
* `ledger`: `goal/next/done/decisions/open_questions` partial-replace JSON, per-project file, compaction re-anchor, disable switch, independent budget.
* Done when: facts survive sessions, notes require approval before searchable, ledger re-anchors after compaction — all `0600` under `data/state/true-knowledge/`.

## MVP4 — human UX + hardening (deferred)

* Rich terminal: fuzzy picker, `history`, `completion-install`, man pages, `status --watch`.
* Evals: frozen-SHA `PASS/PARTIAL/FAIL` + tokens/tool-calls for 27B + <8B filter smoke.
* Ops: diagnostics (`trajectory.ndjson`), resource-limit surfacing, packaging (`brew/nix/npm`), team `.zst` cadence.
* Done when: human-only register→index→explain→sync completes with TAB everywhere, no agent.

## Order logic

Cross-repo rides MVP2 (one extra CBM pass, not a new store). Memory rides MVP3 (new stores + review risk). Human polish rides last (Cobra-standard already usable; agent correctness gates the rest).
