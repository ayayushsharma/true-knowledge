---
id: 01-architecture
title: Architecture — thin shipper over CBM, managed backends, trace log
status: authoritative
date: 2026-09-30
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md, AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md, AGENT_DOCS/history/DECISIONS/2026-09-28-tk-owns-the-binary-and-the-quiesce.md, AGENT_DOCS/history/DECISIONS/2026-09-28-delegate-backend-install-to-vendor.md, AGENT_DOCS/history/DECISIONS/2026-09-22-thin-tk-over-cbm.md, AGENT_DOCS/history/DECISIONS/2026-09-23-tk-managed-backends.md, AGENT_DOCS/history/DECISIONS/2026-09-23-download-at-install-and-setup.md, AGENT_DOCS/history/DECISIONS/2026-09-23-zoekt-library-not-backend.md, AGENT_DOCS/history/DECISIONS/2026-09-23-explicit-source-search.md, AGENT_DOCS/history/ROADMAP.md §MVP1, AGENT_DOCS/history/DECISIONS/2026-09-27-consolidated-agent-docs.md]
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
command. `tk daemon status|stop` flips `watcher_enabled`; see `02-BOUNDARY.md`.

Close semantics: stdin EOF = instant exit. No flush, no stop, no shutdown
deadline. A second Ctrl-C kills, because `main` calls `stop()` as soon as the
context is done.

## Managed backends

`internal/backends` registry + `internal/installer`. tk installs everything it
needs; no apt, brew, npm, or toolchain at runtime.

* Pin truth = config key `cbm_version_pin` (`X.Y.Z`). Installed version is read
  back from `<cache>/bin/<binary> --version`. Pin validation accepts `X.Y.Z` or
  7–40 hex chars.
* Resolver order: `TK_CBM_BIN` > config `cbm_binary` > sibling of the `tk`
  executable > `<cache>/bin` > `PATH`. The managed copy wins over `PATH` so
  `tk install` takes effect.
* Install is tk's own. tk streams the archive for the pinned tag, verifies
  SHA-256 against the tag's `checksums.txt` **before** opening the archive,
  extracts the binary entry to `<cache>/bin/<binary>.new`, and only then
  validates the staged file by asking it for `--version`. Nothing touches the
  live path until the candidate has proven it is the pin.
* The swap is transactional: stage `.new`, validate, rename the current binary
  to `.prev`, rename `.new` into place, validate again, and roll back to `.prev`
  on any failure. A failed swap leaves the previous image serving and the staged
  candidate consumed, never a half-written binary.
* The archive is never buffered whole in memory: a backend release is hundreds
  of MB. It streams to `<state>/tmp` while the digest is computed inline.
* Daemon quiescence is a precondition, not a side effect. Before a replacement
  tk asks the *installed* binary `daemon status`; if one is running it runs
  `daemon stop` first, and only restarts afterwards if it was running. A binary
  swapped under a live daemon is one the daemon's build-identity gate refuses to
  admit, so every later command would fail.
* A daemon that refuses to stop — CBM refuses while clients are committed — is a
  **failed install**. tk prints the committed pids, fetches nothing, writes
  nothing, and exits non-zero. There is no flag that overrides the refusal.
* `daemon start` creates a *permanent* daemon, so tk only calls it to restore one
  that was already running. A first install leaves no daemon behind.
* Stopping is scoped to tk: the daemon is namespaced by `CBM_RUNTIME_DIR`, which
  tk points at `<state>/rendezvous`, so this can never stop an account-wide
  daemon a consumer laptop is running. Stopping also crosses versions — CBM
  handles control-plane requests ahead of its build check, so the newly
  installed binary can stop an older daemon.
* `--check` and `--dry-run` are reports: they never stop a daemon, and an
  up-to-date no-op leaves a running daemon alone. Only a real replacement
  quiesces.
* `tk setup` quiesces the same way but stays fail-open: a busy daemon is one
  clipped line and the agent continues.
* Adding a backend is one registry entry (name, repo, binary, asset naming, tag
  scheme, checksum manifest name). No per-backend code paths in installer,
  status, or `tk install`.
* Bytes are never rebuilt or stripped by tk. Trust comes from the manifest
  verification tk performs at install time.
* Never `go:embed` a multi-hundred-MB executable. Never compile anything on the
  user's machine.
* tk never replaces *itself*. `install.sh` / `install.ps1` at the repo root do
  that, because on Windows the image is locked and on Linux the running inode
  survives a rename. They call `tk install cbm` and hold no CBM version input.

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

The C engine needs an 8MB main-thread stack, and the OS sizes that thread from
the *parent's* inherited `RLIMIT_STACK` (macOS ARM64 and tightened launchd
policies default to 512KB, and an overflow aborts a pipeline pass mid-run).
The wrapper enforces a floor: a soft limit below 8MB gets the spawn rebuilt
through `/bin/sh -c 'ulimit -s <hard> && exec "$0" "$@"'`, capped at the
inherited hard limit; a floor-met parent is spawned directly. See
`AGENT_DOCS/history/DECISIONS/2026-09-28-cbm-stack-floor-at-spawn.md`.

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

## Latency is a spawn-count problem

The CBM coordination daemon does not answer tool calls: it owns watchers,
shared indexing *jobs*, the UI, and session lifecycle, there is no socket or
client flag, and `02-BOUNDARY.md` bans a tk-side resident worker — a ban the
2026-09-30 resident ADR retires when it lands. The only warm transport is a
long-lived MCP stdio child, the daemon-*backed* path.

Every `cli` spawn nonetheless re-runs the engine's version-cohort admission
handshake, so cost is coordination, not query. On CBM 0.11.0 with TensorFlow
indexed a read command takes ~4.9s: 4.7ms to spawn, ~110ms to query, ~4.9s in
a 1ms `nanosleep` poll on a lock `cli` mode is documented never to need. It is
independent of store size — a 2.3MB project costs the same as a 686MB one. A
long-lived child answers in 74ms. Measure, don't assume: `mise run bench`.
No `tk bench` verb; the Unix rule applies. Reasoning and numbers:
`AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md`.
Three traps that make such a number wrong rather than imprecise:

* **`tk.log` under-counts spawns.** `runEnvelope`'s exit-0-no-envelope
  fallback re-runs a tool outside every traced call, so an engine that does
  not honor `--json` costs two spawns and records one. `tests/bench` layer F
  reports logged and observed counts side by side for this reason.
* **Layers are comparable only if they asked the same question.** `tk find`
  routes a query with regex metacharacters to `search_code` and a bare
  identifier to `search_graph`, so `live.Demo` measures grep. Pin a bare one.
* **Spawn the engine with tk's env.** CBM resolves its store from
  `CBM_CACHE_DIR`, so execing the binary without it measures an empty database
  and reports "project not indexed".

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
