---
id: 03-commands
title: CLI surface — every verb, flag, alias
status: authoritative
date: 2026-09-28
supersedes: [AGENT_DOCS/history/compatible-implementation-spec.md §8, AGENT_DOCS/history/DECISIONS/2026-09-28-delegate-backend-install-to-vendor.md, AGENT_DOCS/history/compatible-implementation-spec.md §8.4, AGENT_DOCS/history/DECISIONS/2026-09-25-flag-only-query-forms.md, AGENT_DOCS/history/DECISIONS/2026-09-26-select-flag-forces-project-picker.md, AGENT_DOCS/history/DECISIONS/2026-09-26-trace-verb-kg-trace-alias.md, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp2-envelope-profiles-facade-validate.md, AGENT_DOCS/history/DECISIONS/2026-09-25-comments-pass-fixes.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

# 03-COMMANDS — the CLI surface

Global flags on every command: `--json`, `--budget int` (0 = config defaults),
`--home string` (same as `TK_HOME`).

Flag-only grammar. Payload and project always arrive as flags. A positional
argument is a hard error, and a last-argument-as-project guess was removed on
purpose.

## Setup and admin

| Command | Flags | Notes |
|---|---|---|
| `tk init` | — | create the four `true-knowledge/` dirs plus default config; idempotent |
| `tk setup` | `--register`, `--name`, `--client`, `--tool-profile`, `--dry-run` | init + install + opt-in register + opt-in client snippet |
| `tk install [backend...]` | `--check`, `--dry-run`, `--update`, `--version` | backend = `cbm`; no argument means all missing. Runs the vendor installer script at the pin; an upgrade drains coordinated CBM sessions and may ask them to exit |
| `tk register <path>` | `--name` | registers a path, never indexes it; name defaults to the directory base |
| `tk migrate` | `--from`, `--dry-run` | moves `~/.tk`, `$TK_HOME`, or `~/Library/Application Support/true-knowledge`; writes a `MIGRATED` marker, refuses a re-run without `--force` |
| `tk status` | — | projects, HEAD, freshness; `--json` adds `head`, `current`, `zoekt_head`, `zoekt_fresh` |
| `tk completion <shell>` | — | `bash`, `zsh`, `fish`, `powershell`; prints, never installs |
| `tk mcp-install` | `--client` (required), `--tool-profile`, `--dry-run` | prints the client snippet; `--client pi\|opencode\|claude\|codex` |
| `tk cbm <tool> [args]` | — | low-level passthrough for admin and debugging; exists so nothing else has to spawn by hand |
| `tk daemon status` / `tk daemon stop` | — | CLI-only, never exposed over MCP |

## Indexing

| Command | Flags | Notes |
|---|---|---|
| `tk index [project]` | `--mode fast\|moderate\|full`, `--fast`, `--full` | no argument = all registered; default `moderate`; writes never carry `format:"json"` |
| `tk sync [project]` | — | no argument = all; a no-op on a clean tree |

## Project-resolving commands

Ten commands resolve a project. Each takes `--project`, `-s/--select`, and
rejects positionals. `--project` wins; `--select` beside it is ignored. Without
`--project`, one registered project is auto-selected, otherwise the fuzzy picker
opens when a TTY is present, `ui.picker` is true, and `--json` is absent.

| Command | Alias | Payload flag | Other flags |
|---|---|---|---|
| `tk find` | `kg_find` | `--query` (required) | `--label`, `--limit` (20) |
| `tk explain` | `kg_explain` | `--symbol` (required) | — |
| `tk grep` | `kg_grep` | `--pattern` (required) | `--regex`, `--files`, `--limit` (20) |
| `tk source-search` | — | `--pattern` (required) | `--files` (zoekt `file:`), `--limit` (20) |
| `tk trace` | `kg_trace` | `--symbol` (required) | `--direction inbound\|outbound\|both` (inbound), `--depth` 1–5 (1) |
| `tk outline` | — | `--file` (required) | `--label`, `--limit` (100) |
| `tk impact` | — | none (project only) | `--direction` (inbound), `--depth` (2), `--limit` (50) |
| `tk arch` | — | none (project only) | — |
| `tk query` | — | `--cypher` (required) | `--limit` (20) |
| `tk validate` | — | `--symbol` (required) | `--limit` (5) |

`search` never shipped: it is the one spec verb in this family with no facade to
alias. `kg_arch` and `kg_query` stay legacy RPC names only, since `arch` and
`query` are first-class verbs.

### Routing and validation rules

* `find` routes on the query shape: a regex goes to grep, an identifier to the
  graph, a natural-language phrase to semantic search. `--label` filters graph
  routes only.
* Local validation runs **before** the CBM gate. A bad `--direction` or
  `--depth` is a hard `[tk]` error, never a silently empty traversal. A bad
  `--regex` fails at compile time, before any spawn.
* `trace` has no `--limit`, because `trace_path` has no limit parameter. tk
  never invents engine parameters. The traversal is bounded by `--depth` 1–5 and
  the character budget.
* `explain` = definition + snippet + callers + callees in one bounded call set.
  Its built-in trace is fixed at `both`, depth 1; use `tk trace` when direction
  or depth matters.
* `validate` resolves a symbol exactly. `a.b.c` does not match `a.b.x`, and a
  near-miss is reported as a near-miss, not as a hit. Both CLI and MCP use one
  matcher, over `QualifiedNames`, never a substring test over rendered text.
* `grep` is CBM text search. `source-search` is Zoekt. They never substitute for
  each other under one name.
* `--select` without a reachable picker is a hard, actionable error naming
  `--project`; it never guesses and never blocks on a pipe.

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
`list_projects`; config keys come from one registry in `internal/config`;
`--client` completes `pi,opencode,claude,codex`; `--tool-profile` completes
`scout,analysis,minimal,memory`. Test with `tk __complete`.

## Exit codes

`0` on success, including a `PARTIAL`-style degraded render. `1` on error, and
on MCP cancellation. `tk mcp` deliberately does not propagate the cancellation
`1` to the client. A version that cannot be resolved fails loudly; it never
falls back silently.
