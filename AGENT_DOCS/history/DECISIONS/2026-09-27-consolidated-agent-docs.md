---
title: Consolidated agent docs — one AGENT_DOCS directory, terse facts, plain rules
status: authoritative
date: 2026-09-27
supersedes: [docs/00-AUTHORITY.md, docs/AGENT-PROFILES.md, docs/CBM-BOUNDARY.md, docs/INDEXING.md, docs/PATHS-CONFIG.md, docs/REMAINING-WORK.md, docs/ROADMAP.md, docs/DECISIONS/2026-09-26-structured-cbm-payloads.md (precedence chain), docs/DECISIONS/2026-09-27-comments-pass-2.md (comments.md retired, open work lives in AGENT_DOCS/08-BACKLOG.md)]
superseded-by: null
---

# Consolidated agent docs

## Problem

The authored doc surface was 4,405 lines across `docs/*.md` (7 files), 30 ADRs
in `docs/DECISIONS/`, `AGENTS.md`, and a 1,116-line frozen spec at the root.
Reading it cost more than the information was worth:

* The seven `docs/*.md` files restated the same rules in different words. The
  parked backlog appeared in three places — the roadmap's ops line, the
  remaining-work file, and seven separate park ADRs.
* The `supersedes` front-matter lists had grown into paragraph-length values,
  because each file recorded every narrowing it had ever absorbed.
* Nothing said how much of a document to read. There was no read order, no size
  budget, and no enforcement of either.
* `00-AUTHORITY.md:60` promised a `docs-lint` gate. No such test existed, so
  the header requirement, the precedence chain, and the "never rewrite history"
  rule were all conventions.

## Decision

Authored docs live in one directory, `AGENT_DOCS/`, split into ten numbered
files plus a machine index, with every superseded document preserved verbatim
under `AGENT_DOCS/history/`.

```text
AGENTS.md                     pointer + the rules that must never be missed
AGENT_DOCS/00-INDEX.md        precedence law, style contract, change procedure
AGENT_DOCS/01-ARCHITECTURE.md thin shipper, managed backends, spawn wrapper
AGENT_DOCS/02-BOUNDARY.md     what tk must never do
AGENT_DOCS/03-COMMANDS.md     every verb, flag, alias
AGENT_DOCS/04-MCP.md          profiles, result shape, absence, budgets
AGENT_DOCS/05-INDEXING.md     modes, discovery, text index, freshness
AGENT_DOCS/06-PATHS-CONFIG.md XDG dirs, config keys, env vars
AGENT_DOCS/07-MEMORY.md       facts, notes, ledger, secrets
AGENT_DOCS/08-BACKLOG.md      milestones, remaining work, parked, rejected
AGENT_DOCS/09-CHECKLIST.md    operating rules and PR gate
AGENT_DOCS/manifest.json      machine index: ids, titles, status, edges
AGENT_DOCS/history/           30 ADRs + 7 old docs + this ADR, verbatim
docs/man/                     generated man pages, unmoved
compatible-implementation-spec.md   v1, frozen, unmoved
```

The numeric prefix *is* the read order, and lexicographic order *is* the
precedence order. A reader or a tool walks `00` through `09` and stops when the
question is answered. Nothing has to know the history to use the tool.

### History is immutable, and that is now enforced

Every file under `history/` is byte-identical to its state in `git HEAD`.
`go test ./internal/docs` fails the build if one changes. This is the machine
enforcement of "never rewrite history" that the old docs-law only asserted.

The consequence is that `history/` files carry **no** `superseded-by` stamp
pointing at the new files, because adding one would edit a frozen file. The
supersession record lives in the `supersedes` header of each new file instead,
and `00-INDEX.md` states the whole mapping. A future reader of a history file
learns it is superseded from `00-INDEX.md`, not from a stamp in the file itself.

### Style: terse facts, plain rules

Tables, flags, config keys, paths, tool lists, budgets, numbers, and PARKED
verdicts are written tersely. Boundary rules, safety rules, and precedence are
written in plain English, because a compressed boundary rule is a misread
waiting to happen and a misread there writes to the wrong database.

This split is a deliberate exception to the general terseness preference, and it
is why `02-BOUNDARY.md` and `09-CHECKLIST.md` read differently from the rest of
the directory. Two rules protect the terse files: never compress a MUST NOT, and
never let a term drift — one word per concept across all ten files, no invented
abbreviations, no arrows, no synonym rotation.

### Enforcement, finally

`internal/docs` adds the `docs-lint` that `00-AUTHORITY.md:60` promised. It
asserts: front-matter on every `NN-*.md`; `id` equals the filename stem; ids are
unique; every `supersedes` and `superseded-by` path resolves on disk; no
pre-consolidation path (`docs/DECISIONS/…`, `docs/ROADMAP.md`, and the rest)
appears anywhere in the repo outside `history/`; `history/` blobs match `git
HEAD`; `manifest.json` lists exactly the files on disk; and no file exceeds 200
lines.

The 200-line cap is the load-bearing one. Without a size budget, a consolidated
document silently becomes the thing it replaced. A file that hits the cap gets
split or becomes an ADR.

## What did not move, and why

`docs/man/` stays where it is, so `cmd/genman` keeps writing to `docs/man` and
the `docs-man` mise task is unchanged. The frozen v1 spec stays at the repo
root, where `00-AUTHORITY.md` and every ADR already cited it. Both are
references, not authored truth, and moving them would have bought tidiness at
the cost of a code change and 31 broken citations for no gain in clarity.

`AGENTS.md` stays at the repo root because that path is auto-loaded by opencode,
Claude Code, and similar tools. It becomes a pointer plus the rules that must
never be missed, with the detail in `AGENT_DOCS/09-CHECKLIST.md`.

## Compatibility

None needed, and none promised. This is a documentation restructure inside a
pre-release tool. The CLI, the MCP surface, every config key, and every payload
are untouched — see `00-INDEX.md` for why none of them is a promise anyway.

Two changes exist outside the documents. The `tk install` long help now names
`AGENT_DOCS/history/DECISIONS` instead of the old decisions directory, and
`docs/man/tk_install.1` was regenerated to match it. `mise run docs-man` is
deterministic, so a second run produces no diff.
