---
id: 03-commands
title: CLI surface — every verb, flag, alias
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-10-02-stderr-is-the-diagnostic-channel.md, AGENT_DOCS/history/DECISIONS/2026-10-02-picker-search-is-fuzzy-and-case-insensitive.md, AGENT_DOCS/history/DECISIONS/2026-10-02-project-routing-is-always-explicit.md, AGENT_DOCS/history/DECISIONS/2026-10-02-cross-repo-fleet-text-search.md, AGENT_DOCS/history/compatible-implementation-spec.md §8, AGENT_DOCS/history/DECISIONS/2026-09-28-failed-install-is-a-failed-command.md, AGENT_DOCS/history/DECISIONS/2026-09-28-delegate-backend-install-to-vendor.md, AGENT_DOCS/history/compatible-implementation-spec.md §8.4, AGENT_DOCS/history/DECISIONS/2026-09-25-flag-only-query-forms.md, AGENT_DOCS/history/DECISIONS/2026-09-26-select-flag-forces-project-picker.md, AGENT_DOCS/history/DECISIONS/2026-09-26-trace-verb-kg-trace-alias.md, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md, AGENT_DOCS/history/DECISIONS/2026-09-25-comments-pass-fixes.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

# 03-COMMANDS — the CLI surface

Global flags on every command: `--json`, `--budget int` (0 = config defaults),
`--home string` (same as `TK_HOME`), `--verbose`, `--quiet`.

Flag-only grammar. Payload and project always arrive as flags. A positional
argument is a hard error, and a last-argument-as-project guess was removed on
purpose.

stdout is the answer and nothing else; stderr is the live channel — progress,
warnings, debug narration, engine refusals. `TK_LOG=off|error|warn|info|debug`
(default `warn`); `--verbose` raises it to debug, `--quiet` caps it at error and
silences progress. Neither touches stdout. Contract: `10-OUTPUT.md`.

## Setup and admin

| Command | Flags | Notes |
|---|---|---|
| `tk init` | — | create the four `true-knowledge/` dirs plus default config; idempotent |
| `tk setup` | `--register`, `--name`, `--client`, `--tool-profile`, `--dry-run` | init + install + opt-in register + opt-in client snippet |
| `tk install [backend...]` | `--check`, `--dry-run`, `--update`, `--version` | eight progress stages on stderr, see below; backend = `cbm`; no argument means all missing. tk downloads and checksum-verifies the pinned release itself. A replacement stops the daemon holding the binary first, and restarts it only if it was running. **Exits non-zero if any backend failed** — see below |
| `tk register <path>` | `--name` | registers a path, never indexes it; name defaults to the directory base |
| `tk migrate` | `--from`, `--dry-run` | moves `~/.tk`, `$TK_HOME`, or `~/Library/Application Support/true-knowledge`; writes a `MIGRATED` marker, refuses a re-run without `--force` |
| `tk status` | — | projects, HEAD, freshness; `--json` adds `head`, `current`, `zoekt_head`, `zoekt_fresh` |
| `tk completion <shell>` | — | `bash`, `zsh`, `fish`, `powershell`; prints, never installs |
| `tk mcp-install` | `--client` (required), `--tool-profile`, `--dry-run` | prints the client snippet; `--client pi\|opencode\|claude\|codex` |
| `tk mcp` | `--tool-profile`, `--detach` | the stdio proxy an agent client runs; stdin EOF is an instant exit. `--detach` returns immediately and leaves a resident holding one warm CBM child — see below |
| `tk cbm <tool> [args]` | — | low-level passthrough for admin and debugging; exists so nothing else has to spawn by hand |
| `tk daemon status` / `tk daemon stop` | — | CLI-only, never exposed over MCP |

### The resident

`tk mcp --detach` is **opt-in and never automatic.** The CLI does not start a
resident, and an absent or dead one is not an error: reads dial the socket and
any failure falls back to the one-shot path, which is correct and about 4.9s
slower. Writes (`tk index`, `tk sync`) always take the one-shot path.

The whole surface is the socket and a pid file under `<state>/`. No `tk session`
verb, no subcommands, no idle timeout — the resident runs until reboot or a
signal, and SIGTERM cleans up.

* **What uses it:** the read commands — `find`, `explain`, `trace`, `grep`,
  `outline`, `impact`, `arch`, `query`, `validate`. `tk index` and `tk sync`
  stay one-shot by design. **`tk cbm` does not use the resident at all**: it is a
  raw passthrough, so it always spawns. That is intentional for debugging, and it
  will not get faster because a resident is running.
* **Stop:** SIGTERM the pid in `<state>/resident.pid`. tk ships no verb; the pid
  file is the verb. Use it rather than `pkill -f` — the argv is
  `tk --home <home> resident`, so a pattern like `tk resident` matches only when
  `TK_HOME` ends in `.tk`, and `pkill -f` otherwise matches *your own shell*.
* **Is it up:** `test -S <state>/resident.sock`, or read
  `<state>/logs/resident.log`, which records the engine pid at startup and one
  line per served call. That log is separate from `tk.log` because it outlives
  the command that spawned it — see `10-OUTPUT.md`.
* **`tk install cbm` does not kill it.** The engine is swapped in place behind the
  same socket. Reads during the swap are refused, not queued, and fall back to a
  spawn — a read queued behind an install is slower than the spawn it avoids.
* One connection carries one request and is closed. The engine is still driven
  one call at a time; only connection handling is concurrent.

Design: `history/DECISIONS/2026-09-30-resident-owns-its-endpoint-and-its-swap.md`.

## Indexing

| Command | Flags | Notes |
|---|---|---|
| `tk index [project]` | `--mode fast\|moderate\|full`, `--fast`, `--full` | no argument = all registered; default `moderate`; writes never carry `format:"json"` |
| `tk sync [project]` | — | no argument = all; a no-op on a clean tree |

## Project-resolving commands

Ten commands resolve a project. Each takes `--project`, `-s/--select`, and
rejects positionals. Routing is always explicit: nothing is inferred, so a bare
invocation fails naming `--project <name>` (with the registered list) and
`--select`. `--project` wins, `--select` beside it is ignored, and only
`--select` opens a menu. An unregistered `--project` is a hard error naming the
register command; `source-search` also names `--all-projects`.

| Command | Alias | Payload flag | Other flags |
|---|---|---|---|
| `tk find` | `kg_find` | `--query` (required) | `--label`, `--limit` (20) |
| `tk explain` | `kg_explain` | `--symbol` (required) | — |
| `tk grep` | `kg_grep` | `--pattern` (required) | `--regex`, `--files`, `--limit` (20) |
| `tk source-search` | — | `--pattern` (required) | `--files` (zoekt `file:`), `--limit` (20, 0 = no limit), `--all-projects` |
| `tk trace` | `kg_trace` | `--symbol` (required) | `--direction inbound\|outbound\|both` (inbound), `--depth` 1–5 (1) |
| `tk outline` | — | `--file` (required) | `--label`, `--limit` (100) |
| `tk impact` | — | none (project only) | `--direction` (inbound), `--depth` (2), `--limit` (50) |
| `tk arch` | — | none (project only) | — |
| `tk query` | — | `--cypher` (required) | `--limit` (20) |
| `tk validate` | — | `--symbol` (required) | `--limit` (5) |

`search` never shipped: the one spec verb in this family with no facade to alias.
`kg_arch` and `kg_query` stay legacy RPC names only, since `arch` and `query` are
first-class verbs.

### Routing and validation rules

* `find` routes on the query shape: a regex goes to grep, an identifier to the
  graph, a natural-language phrase to semantic search. `--label` filters graph
  routes only.
* Local validation runs **before** the CBM gate. A bad `--direction`, `--depth`,
  or `--regex` is a hard `[tk]` error, never a silently empty traversal, and
  `--regex` fails at compile time, before any spawn.
* `trace` has no `--limit`, because `trace_path` has no limit parameter. tk never
  invents engine parameters. Bounded by `--depth` 1–5 and the character budget.
* `explain` = definition + snippet + callers + callees in one bounded call set.
  Its built-in trace is fixed at `both`, depth 1; use `tk trace` when direction
  or depth matters.
* `validate` resolves a symbol exactly. `a.b.c` does not match `a.b.x`, and a
  near-miss is reported as a near-miss, not as a hit. CLI and MCP share one
  matcher over `QualifiedNames`, never a substring test over rendered text.
* `grep` is CBM text search. `source-search` is Zoekt. They never substitute for
  each other under one name.
* `--select` without a reachable picker (non-TTY, `--json`, `ui.picker` off) is a
  hard error naming `--project`. A reachable one filters as you type, fuzzily.

## Memory (tk-owned, no CBM needed)

| Command | Notes |
|---|---|
| `tk mem save <topic> <value>` | `--scope project\|global` (project), `--project` (required for project scope), `--provenance` |
| `tk mem recall <topic>` | `--project` narrows; default is all projects plus global |
| `tk mem review list\|approve\|reject` | nothing is searchable until approved |
| `tk note save <title>` | `--text` or `--body <file>`; `--project` required; lands in the review queue |
| `tk note search <query>` | `--limit` 1–100 (10), `--project`; approved notes only |
| `tk note toc` | `--project`; titles only, budgeted, never bodies |
| `tk note reindex [project]` | rebuilds the FTS index from the markdown sources of truth |
| `tk note review list\|approve\|reject` | |
| `tk ledger update <project> <key> <value>` | appends one immutable entry; key is one of the five |
| `tk ledger get [project]` | latest winning value per key, capped by `budgets.ledger_chars` |
| `tk ledger history <project>` | every entry, chronological, uncapped |
| `tk ledger prune <project>` | human-only; interactive TTY confirmation; refuses off a TTY; no MCP tool |

`tk mem`, `tk note`, and `tk ledger` keep their own `text` key under `--json` and
gain nothing from the engine.

## Completion

Cobra only, standard and dynamic. Projects come from `tk.json` and
`list_projects`; config keys from one registry in `internal/config`; `--client`
completes `pi,opencode,claude,codex`; `--tool-profile` completes
`scout,analysis,minimal,memory`. Test with `tk __complete`.

## Exit codes

`0` on success, including a `PARTIAL`-style degraded render; `1` on error and on
MCP cancellation. `tk mcp` deliberately does not propagate the cancellation `1` to
the client. A version that cannot be resolved fails loudly, never a silent
fallback.

A mutating command that did not do its work is an error; a degraded render is the
one exception. `tk install` therefore exits `1` when any backend failed, after
printing its table: the table is the diagnosis, the exit code is the verdict.
`tk setup` is the deliberate opposite — a missing backend is one `tk install`
away and must never block an agent, so it reports the failure and exits `0`.

## Reading an install failure

tk writes every line of an install failure itself, so the `FAILED` line always
names the cause. The exit status is the verdict; the line is the diagnosis. Eight
numbered stages go to stderr first, so the trace says where it stopped.

```
cbm      FAILED: the CBM daemon has live sessions and refused to stop; tk did not replace the binary it holds.
  daemon: refusing to stop; committed clients: 4242 5150
  Close those sessions, then re-run `tk install` — tk cannot override the refusal, and swapping past it would leave a binary the daemon refuses to admit.
```

The refusal case is the one that matters. tk stops the daemon before it replaces
a binary, and a CBM daemon with committed clients refuses to stop. tk surfaces
the committed pids because those pids are the entire fix, and offers no force: a
binary swapped under that daemon would be refused admission by the daemon's own
build-identity gate, so every later command would fail. Nothing is downloaded and
nothing is written.

Ordinary causes name the manifest or the candidate:

```
cbm      FAILED: checksum mismatch for the codebase-memory-mcp release asset
  manifest 032b33c1…, downloaded 9f1c02ab…
  a mirror or proxy is serving different bytes; set TK_RELEASE_BASE_URL to a trusted base, or retry
```

A checksum is verified before the archive is opened, so a mismatch means nothing
was written. A candidate that verifies but does not report the pin fails the same
way, naming the version it did report. In both cases the previous binary is
untouched, and a swap that fails after staging rolls back. `TMPDIR` points at a
private dir inside the cache for the install, so the ~340 MB does not land on a
small system tmpfs.
