---
title: Per-command `--select` forces the project picker open (extends flag-only forms)
status: authoritative
date: 2026-09-26
supersedes: [docs/DECISIONS/2026-09-25-flag-only-query-forms.md (project-resolution helper signatures and the "explicit `--project` wins → picker" precedence only — the strict flag-only grammar, `cobra.NoArgs` + required payload flags, and no positional sniffing all stand unchanged)]
superseded-by: docs/DECISIONS/2026-09-26-trace-verb-kg-trace-alias.md (the "nine project-resolving commands" enumeration — `trace` is the tenth; the per-command flag, `--project` precedence, and TTY gating stand unchanged)
---

# ADR — `--select` forces the project picker open

## Context

The strict flag-only ADR removed positional project sniffing, so the
interactive picker is now reachable **only** through the ambiguity error
path: with one registered project, `tk arch` auto-defaults and the picker
never appears; with several, the user must first read an error to discover
a menu exists. Project selection is the single most common ambiguity in
multi-repo use, and "help me choose" is an explicit intent that a
zero-flag invocation cannot express without giving up the auto-default
convenience for the one-project case.

## Decision

Add a per-command `-s, --select` flag to the nine project-resolving
commands (`find`, `explain`, `grep`, `outline`, `impact`, `arch`, `query`,
`source-search`, `validate`). It is **not** a root persistent flag, and it
is not added to non-project commands (`index`, `sync`, `status`, `mem`,
`note`, `ledger`, `cbm`, `config`, `install`, `mcp`).

1. **`--project` wins; `--select` is ignored beside it.** No
   `MarkFlagsMutuallyExclusive`: the two flags compose as precedence, not
   conflict, so a script carrying `--project` keeps working and
   `--select` is simply never evaluated.
2. **Without `--project`, `--select` always opens the picker**, including
   with exactly one registered project — the auto-default is bypassed, not
   reinforced.
3. **Hard errors, never silent fallbacks or empty projects reaching CBM:**
   * empty registry → `--select needs at least one registered project — run \`tk register <path>\``;
   * picker unreachable (non-TTY, `--json`, or `ui.picker=false`) →
     `--select needs an interactive TTY (+ !--json + ui.picker); pass --project (registered: ...); see \`tk status\``;
   * user aborts the menu → `picker aborted; pass --project (registered: ...); see \`tk status\``.
4. **Helper signatures** (query.go) become:

   * `resolveProject(c *Ctx, flag string, sel bool) string` — `--project`
     wins; else `sel` skips the auto-default and returns `""`; else
     single-registry auto-default.
   * `requireProject(c *Ctx, flag string, sel bool) (string, error)` —
     `--project` wins → forced picker (TTY-gated) → else the existing
     routing hint.
5. **Agent safety.** An agent passing `--select` over a pipe gets a hard,
   actionable error naming `--project`, never a blocking menu and never a
   guess. Selection stays an explicit human act.

## Consequences

* `tk arch --select` and `tk find --query ProcessOrder --select` work for
  humans; every non-interactive path still requires `--project`.
* `resolveProject` with `sel=true` returns `""` by design — callers must go
  through `requireProject` so the picker/error path runs.
* `resolveProject`/`requireProject` call sites and tests gain a third
  `sel bool` argument (`false` where selection is not requested).
* Man pages for the 9 commands regenerate; MCP tool schemas are unchanged
  (agents keep supplying `project` as a JSON-RPC argument).
* e2e covers the non-TTY `--select` failure, the `--project` precedence
  win, and the absence of `--select` on `index`.
