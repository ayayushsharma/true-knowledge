---
title: Roadmap — MVP1 through MVP4
status: authoritative
date: 2026-09-25
supersedes: [compatible-implementation-spec.md §20, docs/DECISIONS/2026-09-22-thin-tk-over-cbm.md (scope), docs/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md (mvp2 non-fleet scope), docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (mvp3 scope), docs/DECISIONS/2026-09-24-evals-harness.md (mvp4 evals design), docs/DECISIONS/2026-09-24-reindex-embed-cache.md (mvp3 reindex embed mechanics), docs/DECISIONS/2026-09-24-zoekt-staleness.md (zoekt-side freshness shipped), docs/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md (mvp4 human UX scope), docs/DECISIONS/2026-09-25-ledger-append-only-history-prune.md (ledger v2 scope), docs/DECISIONS/2026-09-25-cross-repo-contract-verified.md (cross-repo contract), docs/DECISIONS/2026-09-25-fleet-parked-indefinitely.md (fleet parked), docs/DECISIONS/2026-09-25-delivery-parked-download-scripts.md (delivery parked), docs/DECISIONS/2026-09-25-scout-coverage-before-absence.md (scout coverage-before-absence shipped), docs/DECISIONS/2026-09-25-rrf-tuning-parked-indefinitely.md (rrf tuning parked), docs/DECISIONS/2026-09-25-trajectory-parked-indefinitely.md (trajectory parked), docs/DECISIONS/2026-09-25-fleet-cohort-queries-parked-indefinitely.md (fleet-cohort queries parked), docs/DECISIONS/2026-09-25-resource-limit-surfacing-parked-indefinitely.md (resource-limit surfacing parked), docs/DECISIONS/2026-09-25-ledger-enabled-gate-parity.md (ledger.enabled gate on MCP writes)]
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

## MVP2 — code-intel depth (cross-repo fleet parked indefinitely)

*Why first:* flagged focus + unblocks 27B autonomy. Still delegated to CBM, no new stores.
* `kg_*` facade: `kg_find` (regex→grep / ident→graph / NL→semantic router), `kg_explain` (def+snippet+callers+callees, one call), `kg_grep`; legacy `search/trace/arch/query` compat. Shipped as Cobra aliases over `find/explain/grep`, zero behavior change (see envelope ADR).
* `analysis` profile: `query_graph` (read-only, `LIMIT` + timeout guardrails), `validate` (existence + near-miss). Shipped in MVP1: `get_file_outline` (`tk outline`), `detect_changes → impact` (`tk impact`). `tk mcp --tool-profile scout(11)|analysis(14)|minimal(3)` filters tk-side; `manage_adr` passes through in `analysis`.
* Wrapper: `cbmexec.RunJSON` (`cli --json` envelope unwrapped, legacy fallback) on all read paths; writes stay on legacy `Run`.
* Cross-repo: PARKED INDEFINITELY per `docs/DECISIONS/2026-09-25-fleet-parked-indefinitely.md` (fleet orchestration for `CROSS_*` edges — `--cross/--targets`, N-source link pass, generation record — waits on a listed trigger: upstream generic cross-repo edge classes #56/#398, concrete fleet-wide protocol-edge demand, or a cost collapse; un-parking needs a new ADR. Generic cross-repo call graphs stay out of tk scope permanently). Contract verified upstream (cross-repo-contract ADR): `cross-repo-intelligence` is a per-**source-project** `index_repository` mode — `target_projects=["*"]` expands targets only, an N-repo clique needs **N runs (each member as source)** after fresh bases, scope = `CROSS_*` protocol edges; `get_architecture` reports `cross_repo_links` free.
* Freshness: `head/current/fresh` envelopes shipped in MVP1. Zoekt-side staleness eliminated (auto-refresh + live worktree bytes + delta indexing — zoekt-staleness ADR); **coverage-before-absence enforced in every profile** (scout-tools now annotate empty results like the CLI — scout-coverage ADR); left open: stale-cursor protocol (no tk-issued cursors exist yet).
* Done when: 27B answers `who calls X / what breaks if Y changes / outline Z` in ≤3 calls (single-repo; fleet-wide `CROSS_*` and multi-repo call graphs are parked — see fleet-parked ADR).

## MVP3 — memory layer (tk-owned; CBM has no equivalent)

*Why after:* three new tk-owned stores with review/privacy risk; kept off MVP2's path.

**Shipped (code complete, e2e both modes, real-binary verified):** see `docs/DECISIONS/2026-09-23-mvp3-memory-layer.md`.
* `tk mem`: facts `(scope,project,topic)` UPSERT, global sentinel cross-project, provenance+timestamp, secret-detect → review queue (never silent store), approve/reject.
* `tk note`: `notes/<project>/*.md` front-matter source-of-truth + FTS5 BM25 index + optional embedding fusion; capture→review→approve; title-only budgeted `toc`; `reindex` rebuilds from markdown.
* `tk ledger`: **v2 — append-only** `ledger/<project>.jsonl`, fixed five keys (`goal|next|done|decisions|open_questions`), `get` folds last write per key, `budgets.ledger_chars` is a retrieval-only cap, `history` = complete log, `prune` = human-only TTY-confirm. `ledger.enabled` gate. See ledger ADR.
* Storage: `modernc.org/sqlite` (CGo-free) for `facts.db` + `notes/index.db`; schema-versioned, WAL, 0600. Embeddings via **external** Ollama-compatible `/api/embed` (never bundled); BM25 stays authoritative, RRF fusion, all-embed-failure fallback. `note reindex` embeds through a persistent content-keyed cache (`note_embeds`, schema v3): warm call + ⌈n/64⌉ chunked embeds, per-chunk fail-open, unchanged bodies hit the cache across rebuilds (see reindex-embed-cache ADR).
* MCP `memory` profile: scout(11) + 11 in-process tools = 22 (v1 shipped 9; v2 adds `ledger_get`/`ledger_history`, `ledger_update` gains `key`); no CBM/daemon needed for memory tools.
* Done when: facts survive sessions, notes require approval before searchable, ledger appends verbatim and `get` trims by budget on retrieval — all `0600` under `~/.local/share/true-knowledge/`.
* **RRF tuning knobs: PARKED INDEFINITELY** (future scope — canonical `k=60` + authoritative BM25, revisit on an evals signal; see rrf-tuning ADR). Ledger compaction/re-anchor **resolved by design** — append-only + human prune, see ledger ADR.

## MVP4 — human UX + hardening (deferred)

* **Shipped (this phase):** fuzzy project picker + committed man pages. See `docs/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md`.
  * Fuzzy picker: promptui over `requireProject`'s error path only — TTY-gated (`ui.picker` config), never on `--json`/non-TTY/CI, abort = the identical routing error.
  * Man pages: `spf13/cobra/doc` gen via dev-only `cmd/genman` (mise task `docs.man`) → committed `docs/man/tk*.1`, deterministic.
* **Rejected** (doctrine kept — see same ADR): `history` (trace.go:6; jq reads tk.log, replay would re-run redacted argv), `completion-install` (`tk completion <shell>` prints the script; humans wire their own rc), `status --watch` (`watch -n2 tk status`; `--json` one-shot serves agents).
* Evals: frozen-SHA `PASS/PARTIAL/FAIL` + tokens/tool-calls for 27B + <8B filter smoke. **Design locked (not built):** `docs/DECISIONS/2026-09-24-evals-harness.md` — committed frozen fixture + `tests/evals/run.sh` (fake-CBM default, `TK_LIVE=1` opt-in), rule-based record verdicts, `est_tokens = chars/4` proxy, no `tk eval` subcommand, self-judging 27B scorer deferred to phase 2.
* Ops: diagnostics (`trajectory.ndjson`) **PARKED** per trajectory ADR (ops debt, no consumer); resource-limit surfacing **PARKED** per `docs/DECISIONS/2026-09-25-resource-limit-surfacing-parked-indefinitely.md` (caps stay CBM-internal — query tools never surface them, defaults are `off`, coverage already explains thin indexes; revisit on a listed trigger); packaging (`brew/nix/npm`) + team `.zst` cadence PARKED per `docs/DECISIONS/2026-09-25-delivery-parked-download-scripts.md` (future path: GitHub-release platform binaries + `install.sh`/`install.ps1` download scripts, not built yet).
* Done when: human-only register→index→explain→sync completes with TAB everywhere, no agent.

## Order logic

Cross-repo rides MVP2 (one extra CBM pass, not a new store). Memory rides MVP3 (new stores + review risk). Human polish rides last (Cobra-standard already usable; agent correctness gates the rest).

## Remaining-work backlog

Detail for every deferred/parked item (fleet, ops/packaging, ledger compaction/RRF, stale cursors) plus permanently-rejected proposals lives in `docs/REMAINING-WORK.md` — re-read it when compacting code.
