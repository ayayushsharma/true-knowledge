---
title: Strict flag-only query/project forms — no positional payloads, no project sniffing
status: authoritative
date: 2026-09-25
supersedes: [docs/DECISIONS/2026-09-23-explicit-source-search.md (CLI invocation form only: `tk source-search <pattern> [project]` → flag-only — the explicit-no-magic-routing decision itself stands unchanged)]
superseded-by: null
---

# ADR — strict flag-only query/project forms

## Context

The nine query/project-form commands (`find`, `explain`, `grep`, `outline`,
`source-search`, `validate`, `query`, `impact`, `arch`) mixed positional
payload args with an optional trailing `[project]` positional. Project
resolution "sniffed" the last positional against the registry
(`resolveProject(c, flag, args)`) as a fallback when no `--project` was
given. For agents and shells that surface flags, the positional grammar is
ambiguous and forces ad-hoc quoting and ordering:

* payload and project occupy the same positional space (`tk find Demo demo`);
* the last-arg-as-project sniff is invisible and easy to trip by accident
  (a registered name as an NL query or grep pattern silently re-routes);
* the picker reached through `requireProject`'s error path was the only
  human affordance, keyed on the same fuzzy positional state.

## Decision

The 9 commands are now **strict flag-only** for both project and payload.

1. **PROJECT = ALWAYS `--project`.** No positional `[project]`, no
   last-arg-as-project regression sniffing. `Args` becomes `cobra.NoArgs`
   (or the payload flag requirement), so stray positionals are hard errors.
2. **PAYLOAD = a concept-specific flag, not a positional:**

   | command        | payload flag       |
   |----------------|--------------------|
   | `find`         | `--query <query>`  |
   | `explain`      | `--symbol <sym>`   |
   | `grep`         | `--pattern <pat>`  |
   | `source-search`| `--pattern <pat>`  |
   | `validate`     | `--symbol <sym>`   |
   | `query`        | `--cypher <q>`     |
   | `outline`      | `--file <file>`    |
   | `impact`       | *(project only)*   |
   | `arch`         | *(project only)*   |

   Payload commands use `cobra.NoArgs` + `MarkFlagRequired` on the payload
   flag, preserving the old `MinimumNArgs(1)` strictness. Cobra aliases
   (`kg_find`/`kg_explain`/`kg_grep`) are unchanged.
3. **Helper signature change** (query.go): the positional `args []string`
   sniffing is removed entirely.

   * `resolveProject(c *Ctx, flag string) string` — `--project` wins; else
     single-registry auto-default; else `""`.
   * `requireProject(c *Ctx, flag string) (string, error)` — `--project`
     wins → picker (TTY-gated) → else the exact routing hint
     `pass --project (registered: ...); see \`tk status\``.
   * Fail-open + picker gating (`pickerEnabled`/`pickProject`) semantics are
     unchanged; `--project` itself is never `MarkFlagRequired`, so an empty
     project still routes through the picker/hint path.
4. **Completion moves to flags.** The old positional `ValidArgsFunction`
   project completions move to `RegisterFlagCompletionFunc("project", ...)`
   (`outline --file` keeps its file-extension directive on the `--file`
   flag). It stays registry-driven and instant.

## Consequences

* `tk find --query ProcessOrder --project demo`; `tk impact --project demo`;
  `tk query --cypher "MATCH..." --project demo`. Positional project
  invocation now fails with `unknown command`/`unknown flag`-style errors
  instead of silently resolving.
* Tests update to the flag-only contract: `requireProject(ctx, "")` with 2
  registered → error; `requireProject(ctx, "demo")` → `"demo"`; single
  registered + `""` → auto-default.
* Man pages regenerated (`go run ./cmd/genman`, pinned `genDate` so only the
  9 affected pages change). No MCP tool schemas changed — agents already
  receive `project`/`pattern`/`symbol`/`query`/`file` as JSON-RPC arguments.
* Shell-visible breaking change for hand-written `tk <cmd> <payload> [proj]`
  invocations; flagged in this ADR as intentional ergonomics, not a
  regression.