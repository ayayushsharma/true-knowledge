---
title: MVP4 human UX — fuzzy project picker + committed man pages (history/completion-install/status --watch rejected)
status: authoritative
date: 2026-09-24
supersedes: [docs/ROADMAP.md §MVP4 (bare bullets → defined scope)]
superseded-by: null
---

# ADR — MVP4 human UX scope

## Context

ROADMAP MVP4's "human UX" bullet reads "Rich terminal: fuzzy picker, `history`,
`completion-install`, man pages, `status --watch`". Each item was reviewed
against two standing constraints:

* `internal/trace/trace.go:6` — "no `tk log` command will ever exist —
  tail/grep/jq already read it better".
* `docs/CBM-BOUNDARY.md:39` — "If standard Unix commands already do it, tk
  doesn't reimplement it: no log reader, no pager, no grep-wrapper. A new tk
  command needs a reason Unix can't serve."

## Decisions

### Shipped this phase

1. **Fuzzy project picker — build.** The one item Unix can genuinely not
   serve. Every graph command (find/explain/grep/source-search/outline/impact/
   arch/query/validate) needs a project; with >1 registered and no `--project`
   there is exactly one integration point — `requireProject`'s error path
   (`internal/cli/query.go:31-36`). When the picker is unavailable the error
   text is byte-identical to today, so `--json`, non-TTY, and CI behavior never
   change. More than one project is the only trigger; `resolveProject`'s
   single-project auto-pick and flag/arg fast paths stay untouched.
   * Library: `github.com/manifoldco/promptui` (pinned, static tree).
   * Gate: `!--json` && config `ui.picker` (default true) && stdin+stdout are
     character devices. Detection via `os.ModeCharDevice` on `Stat()`, no extra
     TTY dependency; promptui owns raw-mode input.
   * Abort (Esc/Ctrl-C) returns the exact existing "pass --project" routing
     error — interactive refusal and inert failure are indistinguishable.
   * promptui draws straight to os.Stdin/os.Stdout, bypassing the root tee, so
     the picker UI never leaks into tk.log `output.text`.

2. **Man pages — build.** `man(1)` is the standard Unix help interface; ~/.1
   files generated from the Cobra tree are an artifact, not a reimplementation.
   `spf13/cobra/doc` (official cobra-org) → committed `docs/man/tk*.1`, built by
   a dev-only `cmd/genman` (never in tk's command tree) via a `mise` task.
   Determinism: `DisableAutoGenTag` + fixed header so generated pages diff clean.

### Rejected (doctrine kept)

3. **`history` — rejected.** `trace.go:6` stands. `tail/grep/jq` read the
   jq-native tk.log better than any formatter we could ship, and a `--rerun`
   replay would re-execute redacted argv (`[REDACTED]` from secret-shaped args)
   — unsafe at best. ROADMAP bullet struck.

4. **`completion-install` — rejected.** Cobra's built-in `tk completion
   <shell>` already prints the exact script to stdout; humans wire
   `eval`/`source` into their own rc however they want. Writing rc files
   outright is hand-writing user config — fragile (missing `~/.zshrc`,
   `compinit` ordering, unknown shells) and out of CBM-BOUNDARY's
   "tk never writes client/user config" discipline. ROADMAP bullet struck.

5. **`status --watch` — rejected.** `watch -n2 tk status` already re-renders
   the table; `tk status --json` is a lossless one-shot for agents. A polling
   loop inside tk is a Unix reimplementation with no agent demand. ROADMAP
   bullet struck.

## Consequence

* MVP4 human UX (this phase) = fuzzy picker + man pages.
* `ui.picker` joins the config key list (default true) and PATHS-CONFIG is
  updated.
* ROADMAP MVP4 section is rewritten: shipped vs struck, with this ADR cited.
* Done-when unchanged: human-only register→index→explain→sync completes with
  TAB everywhere; the picker only removes the need to recall exact project
  names.