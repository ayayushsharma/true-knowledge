---
id: 00-index
title: Docs index, precedence law, and the doc-writing contract
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/00-AUTHORITY.md, AGENT_DOCS/history/AGENT-PROFILES.md, AGENT_DOCS/history/CBM-BOUNDARY.md, AGENT_DOCS/history/INDEXING.md, AGENT_DOCS/history/PATHS-CONFIG.md, AGENT_DOCS/history/REMAINING-WORK.md, AGENT_DOCS/history/ROADMAP.md]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. Precedence law lives here.

# 00-INDEX — how truth works in this repo

Current truth for humans and agents. Read top to bottom, stop when the question
is answered. `manifest.json` is the machine-readable form of this table.

| File | Covers |
|---|---|
| `00-INDEX.md` | this file — precedence, style contract, how to change a doc |
| `01-ARCHITECTURE.md` | what tk is, thin-shipper split, managed backends, trace log |
| `02-BOUNDARY.md` | what tk must never do (plain English, safety) |
| `03-COMMANDS.md` | every verb, flag, alias, exit behavior |
| `04-MCP.md` | profiles, tool lists, result shape, absence rules |
| `05-INDEXING.md` | index modes, discovery order, watcher, text index, freshness |
| `06-PATHS-CONFIG.md` | XDG dirs, config keys, env vars, migration |
| `07-MEMORY.md` | facts, notes, ledger, embeddings, secret gate |
| `08-BACKLOG.md` | milestones, remaining work, parked and rejected items |
| `09-CHECKLIST.md` | operating rules and PR gate for agents (plain English, safety) |
| `history/DECISIONS/*.md` | 31 dated ADRs, verbatim, immutable |
| `history/*.md` | 7 pre-consolidation docs, verbatim, immutable |
| `manifest.json` | machine index: ids, titles, status, edges |

`docs/man/tk*.1` stays where it is: generated man pages, rebuilt with
`mise run docs-man`. `compatible-implementation-spec.md` stays at the repo root:
v1, frozen, powerless on conflict.

## Precedence

Highest first:

1. `AGENT_DOCS/00-INDEX.md`
2. `AGENT_DOCS/NN-*.md` in lexicographic order (later number wins on conflict)
3. Newest `AGENT_DOCS/history/DECISIONS/YYYY-MM-DD-<slug>.md` — an ADR dated
   after a file's `date:` header still wins over that file
4. `AGENT_DOCS/history/*.md` and the ADRs below the top-level files
5. `compatible-implementation-spec.md` — frozen history, powerless on conflict

## tk is pre-release. Nothing machine-readable is a compatibility promise.

`tk` is unreleased, unversioned against a stability contract, and has no users
to break. Treat that as a licence to fix the design rather than carry a wrong
one. Stated once here so no individual ADR has to repeat it:

* **`--json` envelopes and `tk mcp` result shapes carry no guarantee.** Keys may
  be added, removed, or reshaped in any release, including patches. There is no
  `envelope_version`, no `$schema`, no v1/v2 discriminator. Adding one needs a
  new ADR. "The shape changed" is a supported outcome, not a defect.
* **Adoption is source-level.** A caller who needs a shape to hold still pins a
  commit, and the docs at that commit are the contract. CBM's payload passes
  through verbatim (`04-MCP.md`), and CBM is a separate project on a separate
  clock — tk cannot promise its contents.
* **What is protected:** the human CLI surface (rendered tables, exit codes,
  flag names, help text) and the boundary rules in `02-BOUNDARY.md`. `tk find`
  prints a table forever, and tk never touches SQLite.
* **The one thing not excused:** changing the human surface to fix a
  machine-facing problem, or letting the two faces drift. A change to a machine
  output must leave the human output byte-identical. Dual-path tests in
  `internal/cli` enforce it.

## Style contract

* Tables, flags, config keys, paths, tool lists, budgets, numbers, and PARKED
  verdicts: terse. Fragments, no articles, no filler.
* Boundary rules, safety rules, and precedence: plain English. Never compress a
  rule whose misreading breaks a boundary or flips a MUST NOT.
* No invented abbreviations. No arrows. No synonym rotation — one term per
  concept across the whole directory. Keep `never`, `not`, `only`, `except`.
* Numbers and units exact. Error strings and identifiers verbatim.

## Changing a doc

1. Behavior change, new key, new flag, new tool, new limit: add
   `history/DECISIONS/YYYY-MM-DD-<slug>.md`, then bump the affected
   `NN-*.md` header (`date`, `supersedes`) and add the rule to its body.
2. Never rewrite a file under `history/`. It is immutable and `docs-lint`
   (`internal/docs`) fails the build if a byte changes.
3. Never edit a `NN-*.md` to correct a shape tk no longer emits, unless the same
   change is bumping that file's header. A frozen record keeps the shape it
   described; the new ADR carries the correction.
4. A file over 200 lines is a bug: split it or write an ADR. `docs-lint` fails.
5. Stale paths are bugs. Every pre-consolidation doc path is forbidden outside
   `history/`, and the exact list is declared in `manifest.json` under
   `enforcement.forbidden_paths_outside_history`. `docs-lint` fails.

## Enforcement

`go test ./internal/docs` is the doc gate. It checks: front-matter on every
`NN-*.md`; `id` equals the filename stem; ids unique; every `supersedes` and
`superseded-by` path resolves; no forbidden pre-consolidation path anywhere in
the repo outside `history/`; `history/` blobs byte-identical to `git HEAD`;
`manifest.json` lists exactly the files on disk; no file over 200 lines.

Full gate: `gofmt -l . && go vet ./... && go test ./...`
