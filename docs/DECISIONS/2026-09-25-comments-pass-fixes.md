---
title: Comments-pass fixes (nested mcp config, zoekt freshness, plain-dir filters, streaming installer, cancellation)
status: authoritative
date: 2026-09-25
supersedes: [docs/PATHS-CONFIG.md §config shape (mcp_profile flat key), docs/INDEXING.md (plain-dir index = whole tree), internal/config/config.go (flat mcp_profile), internal/zoekttext (IndexDir walked everything)]
superseded-by: null
---

# ADR — comments-pass fixes (batch)

## Context

A second review pass over `comments.md` surfaced six fixes and one doc-only
question. Each is a real gap, none reworks an earlier decision:

1. `tk sync` called a project clean as soon as the git HEAD (or plain-dir
   fingerprint) matched the registry — even when the zoekt text index had
   never been built or covered an older state. "Clean" must mean *both*
   backends cover the live tree, else `source-search` serves stale data.
2. Plain-dir indexing walked every file, so `venv/`, `__pycache__/`,
   `vendor/` etc. bloated shards with pure noise; there was no way to ignore
   paths on non-git trees (git repos already get `.gitignore` from zoekt's
   git indexer at this pin).
3. The in-memory Stores could nil-panic: a zero-value `Store` (or one built
   directly, bypassing `NewStore`) crashed on the first call instead of
   returning the documented "disabled" error.
4. `tk mcp` served with `context.Background()` and `main` never wired
   SIGINT/SIGTERM into the command context, so a Ctrl-C could not unwind a
   long index/install/serve cleanly.
5. `tests/e2e_mvp1.sh` used GNU-only `stat -c '%a'`, breaking the portability
   contract on macOS.
6. `mcp.profile` was stored as a flat top-level `"mcp_profile"` key — the
   only dotted config key with a non-nested home, inconsistent with
   `budgets`/`embedding`/`ledger`/`ui`.
7. (`installer`) `internal/installer` buffered the whole archive in memory
   before verifying/extracting — a 260 MiB pin peaked ~3× that in RAM.
8. Doc-only: does the append-only ledger need concurrency work? No (below).

## Decision

* **sync = both backends fresh.** `cmdSync` now calls a pure helper
  `syncFresh(p)`: git repos are clean iff `HEAD == p.Head && p.ZoektHead ==
  HEAD`; plain dirs iff fingerprint matches **and** `p.ZoektHead == "files"`.
  A project indexed before zoekt existed is dirty and gets a text-index
  backfill on next sync — it can never be reported clean while
  `source-search` would serve a stale index.
* **Plain-dir filters.** `IndexDir` now accepts an `ignores` pattern list and
  skips a core set of dependency/generated dirs unconditionally: `.git/.hg/
  .svn` (VCS), `node_modules`, `vendor`, `venv`/`.venv`, `__pycache__`,
  `.tox`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.next`, `.nuxt`.
  Lockfiles stay indexable on purpose — they are small, exact-pinned, and
  useful search targets. A new global ignore file `<config>/ignore`
  (`paths.IgnoreFile()`) applies a gitignore-lite line format (`#` comments,
  leading `/` = root-anchored, trailing `/` = dirs only, otherwise a
  component match at any depth) to non-git trees. No pattern beyond core
  skips applies to git-repo indexes (documented limitation of the zoekt pin:
  `.gitignore` is read by the git indexer for those).
* **Nested `mcp` object.** Config now carries `mcp: {profile}` like every
  other dotted group. A custom `UnmarshalJSON` keeps `DisallowUnknownFields`
  and folds the legacy flat `"mcp_profile"` scalar (present-only-when-nested-
  empty), so old configs load and migrate on next `Save`. `version` stays 1.
  Dotted accessor unchanged: `tk config set mcp.profile analysis`.
* **Cancellable everywhere.** `cmd/tk/main.go` uses
  `signal.NotifyContext(…, os.Interrupt, syscall.SIGTERM)` +
  `root.ExecuteContext`; `tk mcp` serves with `cmd.Context()` so Ctrl-C/
  SIGTERM unwinds the server (and everything downstream already gates on ctx:
  zoekt, installer, CLI loops). No `$/cancelRequest` handling is added.
* **POSIX e2e.** The two 0600 checks in `tests/e2e_mvp1.sh` use small
  python3 mode-bit assertions instead of GNU `stat -c '%a'`.
* **Streaming installer.** `Install` fetches a small sha256 manifest, then
  streams the archive straight to a temp file under `<cache>/bin` while
  hashing inline (guarded by `maxArchiveBytes = 512 MiB`), verifies, extracts
  the binary from the temp file, and renames — peak memory is one read buffer,
  not the archive size.

## Consequence

* `config list`/`--json` change shape: `mcp_profile` → `mcp.profile`. Old
  configs still load; nothing prompts a manual migration. (`version` stays 1
  because both shapes coexist on read.)
* Projects that predate zoekt now need one `tk sync` to build text indexes;
  CI gates that asserted "clean HEAD ⇒ no-op" no longer false-positive.
* Plain-dir search results exclude dependency noise; `source-search` stays
  fast and `tk sync` stays the only backfill trigger.
* Ledger concurrency is documented as safe-by-construction (docs cargo):
  appends are `O_APPEND` single-writer lines where any interleaving is a
  valid chronology; `get` folds latest-wins under any order; reads tolerate a
  torn trailing line (dropped); only the single-threaded MCP dispatch writes
  today. No locks/sqlite/fsync-on-append added.
* e2e keeps 0600 assertions everywhere and gains a live check that plain-dir
  sync now requires zoekt built (`--json` shows `zoekt_head: "files"`).

## Rationale not taken

* Full gitignore glob semantics in `<config>/ignore` — out of scope; the
  simple an・chored/component/dir-only kernel covers the practical cases, and
  git repos (the common case) already have `.gitignore`.
* Making plain-dir sync incremental (mtime-scoped rebuild) — no benefit at
  the sizes tk allows non-git trees; full rebuild is fast and always correct.
* `vendor/` skip at any depth could miss a legit monorepo `vendor/pkg/` tree
  — accepted: those belong in a git repo with `.gitignore`, and nesting is
  rare.

## Tests

* `internal/cli` `TestSyncFresh` (git+plain, zoekt-satisfied and stale).
* `internal/config` nested-write/legacy-load round-trip tests.
* `internal/zoekttext` core-skip + ignore-file prune + lockfile-stays.
* `internal/memory` nil-store guards (`TestStoreNilSubstoresErrorNotPanic`).
* `internal/installer` streamed-download test unchanged (260 MiB pin) — now
  bounded memory.
* e2e: 0600 checks python3-based; plain-dir sync requires `zoekt_head`.