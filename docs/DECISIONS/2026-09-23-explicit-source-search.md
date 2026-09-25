---
title: Explicit source-search over magic routing
status: authoritative
date: 2026-09-23
supersedes: []
superseded-by: [docs/DECISIONS/2026-09-23-zoekt-library-not-backend.md (backend mechanics + distribution sections only), docs/DECISIONS/2026-09-24-zoekt-staleness.md (serving model only: static shards → auto-refresh + live worktree bytes), docs/DECISIONS/2026-09-25-flag-only-query-forms.md (CLI invocation form only: `tk source-search <pattern> [project]` → `--pattern`/`--project` flags — decision content stands unchanged)]
---

# ADR — explicit `source-search`, no magic backend routing

## Context

With two text-search paths (CBM `search_code`, Zoekt trigram index), the
tempting design is silent routing: `tk find`/`tk grep` pick a backend per
query shape. That is magic the agent can't see — a misroute class of bugs
with no visibility in the call history.

## Decision

* **No magic routing.** `tk source-search <pattern> [project]` is explicitly
  Zoekt-backed (`--files` maps to Zoekt `file:`, `--limit` bounds matches).
  `find`/`grep` stay CBM-backed with unchanged meaning.
* **Hard errors, no silent fallback:** Zoekt absent → `run tk setup`;
  shards missing → `run tk index`. Never answer from the other backend under
  the same command name — differently-scoped results masquerading as one
  command is worse than an error.
* **Zoekt scope:** literal/regex text search only. Structural questions stay
  on CBM — Zoekt never answers graph queries. No ctags (symbol ranking is
  CBM's job), no `zoekt-webserver` (servers violate no-daemon).
* **Refresh is a function of HEAD, not a service:** `tk index`/`tk sync`
  run `zoekt-git-index -incremental` (git; automatic no-op when SHA covered)
  or `zoekt-index` (plain dirs). Shards live at `<cache>/zoekt/<project>/`;
  per-project `zoekt_head` is tracked in `tk.json` and surfaced in
  `tk status --json`. Zoekt lags → callers use CBM `search_code`, never stale
  shards.
* **MCP mirrors CLI:** `source_search` tool (pattern+project required, same
  hard-error rule). Scout profile grows 8→9 tools — still comfortable for 27B.

## Distribution security (no on-machine compile, ever)

Upstream publishes source only, so tk's release job (CI) — never the user's
machine — compiles the three CLIs per platform from the pinned commit and
publishes `zoekt-<os>-<arch>.tar.gz` + `checksums.txt` under tag
`zoekt-<pin>`. Rationale:

* **Adoption:** requiring a Go toolchain at install time would kill adoption;
  `tk setup` must work on a bare machine with only network access.
* **Security:** per-machine compiles are the weaker posture — heterogeneous
  toolchains, no attestation, and users cannot practically verify what they
  built matches audited source. Centralized CI builds give one auditable
  pipeline: pinned Go toolchain, `-trimpath`, `go.sum`-verified modules,
  published checksums (SLSA provenance later). Users who want to audit can
  rebuild from the pinned commit with the documented command — available,
  never required.
* `tk install` enforces this: checksum mismatch aborts with zero partial
  state, and there is intentionally no `tk install --build-from-source` flag.

## Consequences

* Verified surface (against zoekt @153817f6): `-index`, `-incremental`,
  `-index_dir`, `-jsonl` (match lines are base64 — tk decodes for display),
  ~42ms queries, ~5s cold index at k8s scale, instant incremental no-op.
* Future backends follow the same rule: explicit commands, never silent
  routing.
