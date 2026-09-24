---
title: Roadmap — MVP1 through MVP4
status: authoritative
date: 2026-09-24
supersedes: [compatible-implementation-spec.md §20, docs/DECISIONS/2026-09-22-thin-tk-over-cbm.md (scope), docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md (mvp2 non-fleet scope), docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (mvp3 scope), docs/DECISIONS/2026-09-24-evals-harness.md (mvp4 evals design), docs/DECISIONS/2026-09-24-reindex-embed-cache.md (mvp3 reindex embed mechanics), docs/DECISIONS/2026-09-24-zoekt-staleness.md (zoekt-side freshness shipped)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ROADMAP — thin proxy first, memory later, human polish last

Locked decisions: Go-only thin `tk` over CBM, Linux-style `true-knowledge/` dirs everywhere, no supervisor daemon, 27B default agent, Cobra standard-dynamic completion, human UX deferred, cross-repo soon.

## MVP1 — thin proxy + self-installing backends (code complete, real-binary verified)

* `setup/init/register/index/sync/status/find/explain/grep/source-search/outline/impact/arch/query/cbm/daemon/mcp/config/migrate/install/mcp-install/completion`. `daemon` is CLI-only (never MCP).
* One spawn wrapper `internal/cbmexec`: `codebase-memory-mcp cli <tool> --args-file <json>` (raw-JSON argv is deprecated upstream) + env (`CBM_CACHE_DIR`, `CBM_RUNTIME_DIR`, `CBM_ALLOWED_ROOT`) + budget truncation + fail-open. Project is required — tk never sends `""`.
* `tk` installs ALL its external dependencies itself: `internal/backends` registry (CBM today) + `internal/installer` (pinned download, SHA-256 manifest verify, atomic `<cache>/bin` install, no system package managers). Zoekt is NOT a backend — it links in as a Go library (`go.mod` pin; same-language links, cross-language spawns). `tk setup` = init + install + opt-in register/client. Resolver: `TK_CBM_BIN` > sibling > cache > PATH > download.
* MCP 11-tool proxy: scout profile + snippet + `source_search` (zoekt library; a missing index hints `tk index`, never silent fallback).
* `source-search`: explicit Zoekt trigram text (no magic routing), served in-process via `internal/zoekttext` (`gitindex`/`index`/`shards`/`query` at the `go.mod` pin); shards at `<cache>/zoekt/<project>/`, `zoekt_head` tracked in `tk.json`.
* XDG `true-knowledge/{config,data,cache,state}` + `TK_*`/`TK_HOME` isolated test homes.
* No supervisor, no facts/notes/ledger, no graph code in `tk`.
* Done when: `TK_HOME=/tmp/x` smoke (`setup → register → index → sync no-op → status --json → mcp tools/list` = 11) passes with zero `~/.tk`/`Library` writes — verified against real CBM 0.11.0 + zoekt @153817f6 (spawn spike; library port per zoekt-library ADR).

## MVP2 — code-intel depth + cross-repo fleet

*Why first:* flagged focus + unblocks 27B autonomy. Still delegated to CBM, no new stores.
* `kg_*` facade: `kg_find` (regex→grep / ident→graph / NL→semantic router), `kg_explain` (def+snippet+callers+callees, one call), `kg_grep`; legacy `search/trace/arch/query` compat. Shipped as Cobra aliases over `find/explain/grep`, zero behavior change (see envelope ADR).
* `analysis` profile: `query_graph` (read-only, `LIMIT` + timeout guardrails), `validate` (existence + near-miss). Shipped in MVP1: `get_file_outline` (`tk outline`), `detect_changes → impact` (`tk impact`). `tk mcp --tool-profile scout(11)|analysis(14)|minimal(3)` filters tk-side; `manage_adr` passes through in `analysis`.
* Wrapper: `cbmexec.RunJSON` (`cli --json` envelope unwrapped, legacy fallback) on all read paths; writes stay on legacy `Run`.
* Cross-repo: PARKED per envelope ADR (fleet orchestration, `--cross/--targets`, generation record wait for dedicated fleet ADR). Upstream truth stands: one `cross-repo-intelligence` call with `target_projects=["*"]` links the fleet, no tk-driven two-pass loop; `get_architecture` reports `cross_repo_links` free.
* Freshness: `head/current/fresh` envelopes shipped in MVP1. Zoekt-side staleness eliminated (auto-refresh + live worktree bytes + delta indexing — zoekt-staleness ADR); left open: coverage-before-absence enforcement in `scout`, stale-cursor protocol (no tk-issued cursors exist yet).
* Done when: 27B answers `who calls X / what breaks if Y changes / outline Z` in ≤3 calls; 2-fixture fleet links `CROSS_HTTP_CALLS`.

## MVP3 — memory layer (tk-owned; CBM has no equivalent)

*Why after:* three new tk-owned stores with review/privacy risk; kept off MVP2's path.

**Shipped (code complete, e2e both modes, real-binary verified):** see `docs/DECISIONS/2026-09-23-mvp3-memory-layer.md`.
* `tk mem`: facts `(scope,project,topic)` UPSERT, global sentinel cross-project, provenance+timestamp, secret-detect → review queue (never silent store), approve/reject.
* `tk note`: `notes/<project>/*.md` front-matter source-of-truth + FTS5 BM25 index + optional embedding fusion; capture→review→approve; title-only budgeted `toc`; `reindex` rebuilds from markdown.
* `tk ledger`: bounded full-text-replace JSON per project (`ledger/<project>.json`), `ledger.enabled` gate + independent `budgets.ledger_chars` budget.
* Storage: `modernc.org/sqlite` (CGo-free) for `facts.db` + `notes/index.db`; schema-versioned, WAL, 0600. Embeddings via **external** Ollama-compatible `/api/embed` (never bundled); BM25 stays authoritative, RRF fusion, all-embed-failure fallback. `note reindex` embeds through a persistent content-keyed cache (`note_embeds`, schema v3): warm call + ⌈n/64⌉ chunked embeds, per-chunk fail-open, unchanged bodies hit the cache across rebuilds (see reindex-embed-cache ADR).
* MCP `memory` profile: scout(11) + 9 in-process tools = 20; no CBM/daemon needed for memory tools.
* Done when: facts survive sessions, notes require approval before searchable, ledger truncates by budget — all `0600` under `~/.local/share/true-knowledge/`.
* Deferred: compaction/re-anchor protocol for ledgers; RRF tuning knobs beyond defaults.

## MVP4 — human UX + hardening (deferred)

* Rich terminal: fuzzy picker, `history`, `completion-install`, man pages, `status --watch`.
* Evals: frozen-SHA `PASS/PARTIAL/FAIL` + tokens/tool-calls for 27B + <8B filter smoke. **Design locked (not built):** `docs/DECISIONS/2026-09-24-evals-harness.md` — committed frozen fixture + `tests/evals/run.sh` (fake-CBM default, `TK_LIVE=1` opt-in), rule-based record verdicts, `est_tokens = chars/4` proxy, no `tk eval` subcommand, self-judging 27B scorer deferred to phase 2.
* Ops: diagnostics (`trajectory.ndjson`), resource-limit surfacing, packaging (`brew/nix/npm`), team `.zst` cadence.
* Done when: human-only register→index→explain→sync completes with TAB everywhere, no agent.

## Order logic

Cross-repo rides MVP2 (one extra CBM pass, not a new store). Memory rides MVP3 (new stores + review risk). Human polish rides last (Cobra-standard already usable; agent correctness gates the rest).
