---
title: Zoekt as a direct Go library, not a backend
status: authoritative
date: 2026-09-23
supersedes: [docs/DECISIONS/2026-09-23-explicit-source-search.md (backend mechanics + distribution only; explicit source-search stands)]
superseded-by: null
---

# ADR — Zoekt links in; only cross-language backends spawn

## Context

The source-search ADR treated Zoekt as an installable backend (registry
entry, CI-built CLIs, download+verify, subprocess spawn). That machinery was
built and verified — then reconsidered: Zoekt is Go, tk is Go. The spawn tax
(fork/exec, shard-open per query, kill-only timeouts, 100MB sidecar per
platform) buys nothing here that a function call doesn't do better.

## Decision

* **Zoekt is a direct library dependency**, not a backend. No registry entry,
  no installer support, no pins in tk config, no resolver, no
  `tk install zoekt`. The pin lives in `go.mod` at pseudo-version
  `v0.0.0-20260911061844-153817f643cd` (`go.sum` records it).
* **Rule going forward: same-language backends link, cross-language backends
  spawn.** CBM (pure C) stays spawned via the single `cbmexec` wrapper. This
  split is by language boundary, not preference.
* **Library mapping** (API verified at the pin: `gitindex.IndexGitRepo(opts)
  (bool, error)`, root `zoekt` + `query` + `index` + `shards` packages):
  new `internal/zoekttext` package with exactly three call sites —
  `IndexRepo` (git via `gitindex` incremental; plain dirs via `index`
  builder), `Search` (`query.Parse` + shard searcher under a timeout
  context, structs out, no base64 round-trip), plus a mutex-guarded searcher
  cache for the MCP server's lifetime. Existing CLI/MCP signatures
  (`ensureZoektIndex`, query/render, `source_search` schema, budgets,
  hard-error rules) are unchanged — bodies delegate.
* **Simplifications that follow:** installer reverts to single-binary form
  (CBM only); config drops the `Backends` map and SHA validation back to
  `cbm_version_pin`; `TK_ZOEKT_BIN`/mirror env and the Zoekt resolver delete.
  `tk setup` installs CBM only — Zoekt arrives inside the `tk` binary.

## Mandatory safety gates (harms mitigations, enforced)

1. **Import audit** at the pin: no `init()` side effects, signal handlers, or
   globals in the imported packages (`query`, `index`, `gitindex`, `shards`,
   root `zoekt`); mains-only side effects (`cmd/`, `maxprocs`, profilers) are
   never imported. Re-run on every pin bump.
2. **`recover()` middleware** at the MCP handler boundary (and index paths)
   mapping panics to `-32000` fail-open errors.
3. **Size caps before every library index call** — the OOM-guardrail that
   replaces process isolation. Kernel-scale input refuses with a clear
   message; `--full`-scale indexing is a CLI activity, never an MCP-server
   one.
4. **Supply chain:** `GOFLAGS=-mod=readonly`, `-trimpath` releases,
   `go mod verify` + `govulncheck` in CI, Apache-2.0 attribution note.
5. **Revert seam:** `IndexRepo`/`Search` stay the only two call sites, so
   unwinding to spawn later is a rewrite of bodies, not callers.

## Consequences

* One static `tk` binary again; zero Zoekt downloads, resolvers, or release
  matrix. The 42ms fork/exec tax and the base64 JSONL round-trip disappear;
  timeouts become context cancellation.
* Net code deletion: the entire Zoekt spawn layer, registry entry, and
  multi-binary machinery remove, replaced by an import and three function
  bodies.
* The explicit-`source-search` doctrine (no magic routing, hard errors,
  Zoekt-never-answers-graph) stands unchanged — only the invocation
  mechanism moves in-process.

## Appendix: deliberate config break (pre-release)

Removing the `Backends` map means `config.json` files written by the brief
spawn-based builds fail strict validation (`unknown field "backends"`) and
fall back to a clear error, not silent defaults. Accepted because no
released version ever wrote that field: the only affected homes are local
`/tmp` test homes. Fix is `rm <config>/config.json && tk init` (or
`TK_HOME=… tk init`). Strictness (`DisallowUnknownFields`) stays — silent
schema drift is worse than a loud, one-time, pre-release break.
