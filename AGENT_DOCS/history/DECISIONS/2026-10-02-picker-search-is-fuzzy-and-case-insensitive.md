---
id: 2026-10-02-picker-search-is-fuzzy-and-case-insensitive
title: The project picker filters as you type — fuzzy and case-insensitive
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-24-mvp4-human-ux-picker-manpages.md (the picker's search capability, described as fuzzy — the library choice, the gate formula, and the abort semantics all stand)]
superseded-by: null
---

# ADR — the picker filters as you type

## Context

`03-COMMANDS.md:79` and `06-PATHS-CONFIG.md:95` called the project picker
"fuzzy". It was not. It had no text input at all.

promptui decides whether a `Select` can search by whether `Searcher` is
non-nil (`select.go:251`, `canSearch := s.Searcher != nil`). `pickProject` set
only `Label`, `Items`, and `Size`, so `canSearch` was false and the widget had
nowhere to put typed characters: `/` hit a `break` that does nothing
(`select.go:264-267`), and every printable rune fell through to `default` and
was discarded (`select.go:291-295`). The help line dropped its search hint
because `renderHelp(canSearch)` was passed `false` (`select.go:302`). Selection
was arrow keys and nothing else, with the list scrolling at `Size: 15` and no
way to narrow it.

This is `AGENTS.md` rule 10 exactly: a documented property was read off the
dependency's feature list rather than off the widget, and the difference only
shows up in front of a human. No test caught it because no test can reach
`pickProject` — `go test` has no TTY, so the picker body is exercised by nobody.

## Decision

**Type-to-filter, case-insensitive, fuzzy.** Two fields on the same widget:

```go
StartInSearchMode: true,
Searcher:          func(query string, i int) bool { return fuzzyFilter(query, labels[i]) },
```

`fuzzyFilter` is a case-insensitive subsequence match over the rendered label:
`DEMO` finds `demo`, `tkk` finds `true-knowledge`, `work/api` finds a project by
its directory. The label carries the path, so matching the label means the path
is searchable for free.

No new dependency. tk ships promptui, and the missing piece was two fields, not
a library.

## Measured in the pinned promptui v0.9.0, not assumed

* **Arrows still work while searching** (`select.go:258,266,286`). `j`/`k`/`h`/`l`
  become query text, because promptui gates vim keys on `!searchMode`
  (`select.go:258-289`). That trade is the price of typing without a prefix
  key, and it is the fzf convention.
* **Zero matches cannot panic.** `Run` loops until `idx != NotFound`
  (`select.go:367-374`), so Enter with no match is a no-op and the list renders
  promptui's "No results". `list.Index()` indexes `scope[cursor]`
  (`list/list.go:150`) and is only reached after that check passes.
* **Backspacing to empty restores the full list** (`CancelSearch`,
  `list/list.go:94-98`).

## Not built: ranking

promptui's `Searcher` is a predicate, `func(input string, index int) bool`
(`list/list.go:12`), and `list.search` appends matches in item order
(`list/list.go:82-92`). So matches come back in the order they were handed over:
`Registry.Names()` is sorted, which means the closest match is usually near the
top but not guaranteed to be first.

Per-keystroke ranking means replacing the widget with a tk-owned raw-mode loop
and its own prompt rendering. That is a terminal in tk, which tk does not own,
and `02-BOUNDARY.md` prefers delegation to reimplementation. Revisit if a real
registry (tens of projects) makes an unranked fuzzy filter feel wrong.

## Consequences

* `pickProject`'s body still has no unit test; `TestFuzzyFilter` covers the
  matcher and `TestPickerSearcherIndexesLabels` covers the index wiring, because
  an off-by-one there would filter the wrong project. The widget itself is a
  manual PTY check.
* `06-PATHS-CONFIG.md`'s `ui.picker` row keeps the word "fuzzy" and is now
  true.
* Selection remains a human act with a TTY, `--json`, or nothing — unchanged by
  this ADR. Who is allowed to reach the picker is
  `2026-10-02-project-routing-is-always-explicit.md`.