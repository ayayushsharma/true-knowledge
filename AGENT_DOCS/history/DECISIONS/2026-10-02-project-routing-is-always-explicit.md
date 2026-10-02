---
id: 2026-10-02-project-routing-is-always-explicit
title: Project routing is always explicit — no auto-select, no auto-opened picker
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-10-02-cross-repo-fleet-text-search.md (only the clause "resolveProject's single-project auto-select keeps serving the other ten project-resolving commands untouched" — the zoekt fleet, the completeness line, and `--all-projects` all stand), AGENT_DOCS/history/DECISIONS/2026-09-26-select-flag-forces-project-picker.md (the helper signatures and the auto-default-bypass rationale — the per-command flag, `--project` precedence, the TTY gate, and the three `--select` error strings all stand), AGENT_DOCS/history/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md (the ambiguity-error-opens-the-menu rule — the library choice, the gate formula, and the abort semantics all stand)]
superseded-by: null
---

# ADR — project routing is always explicit

## Context

Two things were inferred from an argument the caller left out.

1. **A bare query opened a menu.** With no `--project` and no `--select`, and
   any registry other than exactly one project, `requireProject` called the
   picker (`query.go:56-60` before this change). Ten commands shared the branch,
   so `tk arch` on a workstation with five repos would block on a modal list
   that the invocation never mentioned. The user learns a menu exists by being
   shown one, and a human who wanted a specific project has to escape out of it
   first.
2. **A bare query with one registered project silently picked it.**
   `resolveProject` returned `Reg.Names()[0]` (`query.go:26-28`). The command
   then read exactly like an explicit one, on a project nobody named.

The cost is not the interruption, it is the record. `tk arch` in shell history
does not say which project answered, and `tk.log` logs argv — so the two
invocations that differ are the two that look identical. A query whose scope is
unstated is untraceable in the one artifact agents are told to read. Making
absence mean "the registry" is the same class of move the fleet ADR refused for
`source-search`.

The rest of the codebase had already decided this twice. MCP errors on an empty
project (`internal/mcp/server.go:741`, `:874`) and had never inferred.
`resolveSourceScope` never inferred. The CLI was the last surface guessing.

## Decision

**Nothing is inferred. A project is stated or requested, never deduced.**

1. `resolveProject` is deleted. `requireProject(c, verb, flag, sel)` returns
   `--project` when given and otherwise fails.
2. **No auto-open.** The picker is reached only through `--select`. With neither
   flag the command fails; it never blocks, never offers a menu, and never
   reaches CBM with a project.
3. **The error names the verb and every route that exists for it:**

   ```
   [tk] arch needs a project: pass --project <name> (registered: alpha, beta),
   or --select to pick one; see `tk status`
   ```

   `verb` is `cmd.Name()`, so an alias (`kg_find`) reports `find`.
4. **An unregistered `--project` is a hard error**, checked before the CBM gate,
   with one shared wording for all ten verbs:

   ```
   [tk] arch: project "knolwedge" is not currently registered (registered: alpha);
   register it with `tk register <path> --name knolwedge`
   ```

   `source-search` shares the helper. This replaces two different messages, one
   of which (`unknown project %q`) named no way to fix itself. The alternative
   was worse than ugly: `freshness` returns `{"fresh": false}` for a name the
   registry does not hold (`root.go:412`), so a typo could render as a
   confident note about a project that does not exist.
5. **Unchanged, deliberately:** `--project` beats `--select`; `ui.picker`,
   `--json`, and the TTY gate still decide whether a menu is reachable; the
   three `--select` error strings from `2026-09-26` are byte-identical; so is
   the abort mapping.

## Consequences

* `tk arch` in a one-project registry now fails. That is the point: the
  invocation should say what it read.
* `requireProject` grew a `verb` parameter. Nine call sites pass `cmd.Name()`.
* One-project ergonomics move to a shell alias, `tk __complete`, and
  `--select`, not to a silent default.
* `--all-projects` is named only where it exists. The nine CBM verbs get two
  routes; `source-search` gets three. See "Not built".

## Not built

**`--all-projects` on the nine CBM-backed verbs.** Cohort queries stay parked
under `2026-09-25-fleet-cohort-queries-parked-indefinitely.md`: N spawns, and a
per-project-plus-coverage envelope shape tk has never produced. Naming a flag
that errors as unknown on nine commands is worse than naming the two that work.
The error text is written so widening this later is one string.