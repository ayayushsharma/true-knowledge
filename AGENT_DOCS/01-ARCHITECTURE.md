---
id: 01-architecture
title: Architecture — thin shipper over CBM, managed backends, trace log
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-22-thin-tk-over-cbm.md, AGENT_DOCS/history/DECISIONS/2026-09-23-tk-managed-backends.md, AGENT_DOCS/history/DECISIONS/2026-09-23-download-at-install-and-setup.md, AGENT_DOCS/history/DECISIONS/2026-09-23-zoekt-library-not-backend.md, AGENT_DOCS/history/DECISIONS/2026-09-23-explicit-source-search.md, AGENT_DOCS/history/ROADMAP.md §MVP1, AGENT_DOCS/history/DECISIONS/2026-09-27-consolidated-agent-docs.md]
superseded-by: null
---

# 01-ARCHITECTURE — what tk is

One static Go binary, `tk`. Thin shipper over `codebase-memory-mcp` (CBM).
Graph work, text-index work, daemon, watcher, install matrix = CBM. tk adds
paths, config, one spawn wrapper, render, completion, and its own memory layer.

## Split

| tk owns | CBM owns |
|---|---|
| XDG `true-knowledge/` path resolution | Tree-sitter AST (162 langs), Hybrid LSP (10 langs) |
| `config.json` source of truth, env propagation | graph, store (`*.db`), FTS5, embeddings, watcher |
| one spawn wrapper `internal/cbmexec` | coordination daemon, install matrix (45 surfaces) |
| human render, `--json` envelope, `--help` | UI `:9749`, `.zst` artifacts, CBM logs |
| Cobra completion, `tk setup` front door | admission barrier, conflict log |
| memory layer: `mem`, `note`, `ledger` | — (CBM has no equivalent) |

Rule of thumb: **same-language links, cross-language spawns.** Zoekt is a Go
library pinned in `go.mod` (pseudo-version `v0.0.0-20260911061844-153817f643cd`),
so it is linked, not installed. CBM is a C binary, so it is spawned, once, by
one wrapper. There is no `tk install zoekt`.

## No supervisor daemon

tk runs no daemon of its own. CBM's coordination daemon is shared per account
(first-starts, last-stops) and is not tk's to manage from MCP. `cbm cli` mode is
daemon-free: one-shot, a temp worker only for `index_repository`, exits with the
command. `tk daemon status|stop` exists for humans flipping
`watcher_enabled`; see `02-BOUNDARY.md`.

Close semantics: stdin EOF = instant exit. No flush, no stop, no shutdown
deadline. A second Ctrl-C kills, because `main` calls `stop()` as soon as the
context is done.

## Managed backends

`internal/backends` registry + `internal/installer`. tk installs everything it
needs; no apt, brew, npm, or toolchain at runtime.

* Pin truth = config key `cbm_version_pin` (`X.Y.Z`); installed versions tracked
  in `<cache>/bin/.json`. Pin validation accepts `X.Y.Z` or 7–40 hex chars.
* Resolver order: `TK_CBM_BIN` > config `cbm_binary` > sibling of the `tk`
  executable > `<cache>/bin` > `PATH`. The managed copy wins over `PATH` so
  `tk install` takes effect.
* Install streams the archive to a temp file while hashing inline, guard
  `maxArchiveBytes = 512 MiB`, verifies SHA-256 against the release manifest
  **before any write**, then renames atomically. Checksum mismatch aborts with
  zero partial state.
* Mirrors: `TK_RELEASE_BASE_URL_CBM`, falling back to `TK_RELEASE_BASE_URL`.
* Adding a backend is one registry entry (name, repo, binary, asset naming, tag
  scheme). No per-backend code paths in installer, status, or `tk install`.
* Bytes are never rebuilt or stripped by tk. Trust comes from manifest
  verification at install time.
* Never `go:embed` a multi-hundred-MB executable. Never compile anything on the
  user's machine.

`tk install [backend...]` flags: `--check` (report, no network), `--dry-run`
(print plan URLs), `--update` (reinstall even when current), `--version X.Y.Z`
(override the pin, persists to config).

## `tk setup` — the front door

Order: `init` (idempotent) → install all backends at their pins → opt-in
`--register` (current directory, never a default) → opt-in `--client
pi|opencode|claude|codex` snippet. `init` and `install` stay available
separately for scripting. `tk setup --dry-run` prints the whole plan without
touching the network or disk.

Client snippets are printed, never written. `tk mcp-install --client … --dry-run`
is the same thing without `init`/`install`.

## Spawn wrapper

One wrapper, `internal/cbmexec`, and nothing else spawns CBM:

```text
codebase-memory-mcp cli <tool> --args-file <json>
```

Raw-JSON argv is deprecated upstream. Every spawn sets `CBM_CACHE_DIR`,
`CBM_RUNTIME_DIR=<state>/rendezvous`, `CBM_ALLOWED_ROOT`. Project is always
required — tk never sends `""`.

Two read paths over one spawn, deliberately:

| Path | Caller | Engine ask | Return |
|---|---|---|---|
| `RunJSON` | humans (no `--json`) | default tree | envelope unwrapped to text, legacy fallback on any failure |
| `RunStructured` | `--json`, `tk mcp` | `format:"json"` | engine `structuredContent` verbatim under `data` |
| `Run` | writes (`index`, `sync`) | default tree | text, never `format:"json"` |

`format:"json"` is the capability probe: an older engine that ignores it sends no
`structuredContent`, so `RunStructured` returns `Data == nil` with legacy text
and no error. The result is memoized per process. Nothing is ever reported as
structured that is not.

## Trace log

`<state>/logs/tk.log`, unified JSONL, mode `0600`, rotated at 10 MiB ×2. Read it
with Unix tools; there is no `tk log` command.

```bash
tail -n 50 tk.log | jq .                                  # recent calls
jq -c 'select(.exit != 0)' tk.log                         # failures only
jq -r '[.ts, (.argv|join(" "))] | @tsv' tk.log            # argv history
jq -r 'select(.mcp.tool=="source_search") | .output.text' tk.log
```

Redaction: inputs broad, outputs narrow. String tool params and CLI argv get
whole-value `[REDACTED]` when `memory.DetectSecret` flags the value, span-mask
otherwise. Output and error text stay on the narrow `trace.Redact` set, which is
frozen — precision first, so `testRedactLeavesCodeAlone` still holds. Backend
event `Detail` and `Error` are masked too. The widened key-assignment pattern
(`api[_-]?key|secret|password|passwd|token|credential` with `\s*[:=]\s*\S{6,}`)
lives in the memory layer, not in `trace.Redact`.

## Build and test

```bash
mise run build          # dist/tk
mise run docs-man       # regenerate docs/man/tk*.1
gofmt -l . && go vet ./... && go test ./...
tests/e2e_mvp1.sh       # fake-CBM e2e, TK_LIVE=1 for the real binary
```
