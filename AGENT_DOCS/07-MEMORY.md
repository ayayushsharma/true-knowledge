---
id: 07-memory
title: Memory layer — facts, notes, ledger, embeddings, secret gate
status: authoritative
date: 2026-09-27
supersedes: [compatible-implementation-spec.md §7, compatible-implementation-spec.md §8, AGENT_DOCS/history/DECISIONS/2026-09-23-mvp3-memory-layer.md, AGENT_DOCS/history/DECISIONS/2026-09-24-reindex-embed-cache.md, AGENT_DOCS/history/DECISIONS/2026-09-25-ledger-append-only-history-prune.md, AGENT_DOCS/history/DECISIONS/2026-09-25-ledger-enabled-gate-parity.md, AGENT_DOCS/history/DECISIONS/2026-09-24-log-redaction-profile-gate-cancellation.md]
superseded-by: null
---

# 07-MEMORY — the tk-owned layer

CBM has no equivalent, so tk owns all of it. It runs in-process, needs no
spawn, no backend, and no daemon: long-term memory keeps working when the graph
and text backends are down. Available over the CLI (`tk mem`, `tk note`,
`tk ledger`) and over the `memory` MCP profile (11 tools).

Storage is `modernc.org/sqlite`, pinned and CGo-free, with FTS5 built in. Two
databases: `mem/facts.db` and `notes/index.db`. Schema-versioned with
`PRAGMA user_version`, WAL on, files `0600`, directories `0700`.

**Never use FTS5 external-content tables.** At this driver they corrupt on
insert (`database disk image is malformed (267)`). Use standalone FTS5 keyed on
the notes table's `rowid`.

## Facts

`tk mem save <topic> <value>`, upserted on `(scope, project, topic)`. A
`global` scope is the cross-project sentinel. Every fact carries provenance and
a timestamp.

A secret-looking value never lands silently. API keys, tokens, JWTs, private
keys, and high-entropy strings go to the review queue instead, behind the
`memory.SecretMask` layer that sits over `trace.Redact` before any CLI output,
MCP tool output, or `tk.log` write. `tk mem recall <topic>` checks the named
project first, then global, or all projects plus global.

Nothing is searchable until approved. `tk mem review list|approve|reject` is
the gate.

## Notes

Markdown is the durable truth: `notes/<project>/*.md` with front-matter, one
approved note per file. `notes/index.db` is derived and fully rebuildable with
`tk note reindex`. The database is never the source of truth for a note.

```text
tk note save <title> --text "…" | --body <file> --project demo   # → review queue
tk note review approve <id>                                       # → markdown
tk note search <query> --limit 10                                 # approved only
tk note toc                                                        # titles, budgeted
```

### Reindex and the embed cache

`note reindex` drops and rebuilds `notes` and `notes_fts`, and it must never
drop `note_embeds` (schema `user_version` 3, columns `file`, `model`, `digest`
= sha256 of the note body, `embedding` BLOB, primary key `(file, model)`,
indexed on `model`).

Embed order, which matters for cost:

1. A body-digest match hits the cache — zero HTTP.
2. Otherwise one warm call with a throwaway constant body, so a slow model load
   is paid once, not per chunk.
3. Then chunks of 64, giving ⌈n/64⌉ calls.
4. A failed chunk degrades that chunk to keyword-only and nothing else.
5. The cache refreshes in the same insert transaction: vanished files are
   pruned, the rest `INSERT OR REPLACE`, re-keyed by body digest.

A failed warm call skips straight to BM25-only, so a down endpoint never costs
`chunks × timeout`. Embedding HTTP stays outside the insert transaction, always.

## Notes search

BM25 from FTS5 is authoritative. Embeddings are a fail-open enhancement: the
two rank lists are fused with reciprocal rank fusion, `k = 60`, weight-less. Any
embed failure, timeout, or model mismatch degrades to plain BM25, so retrieval
can never be harmed by the defaults.

`k` and fusion weights are parked indefinitely — see `08-BACKLOG.md`.

## Embeddings

An external Ollama-compatible `POST /api/embed` endpoint, opt-in via
`embedding.enabled`. Models are never bundled and never downloaded. Enabling it
requires both an endpoint and a model. `approve` and `reindex` attach the vector
and its model tag.

## Ledger

Append-only JSONL at `<data>/ledger/<project>.jsonl`. The legacy
`<project>.json` blob is ignored by the v2 reader, the first update starts a
fresh log, and `prune` removes both shapes.

One entry is four fields, `{"seq", "ts", "key", "value"}`, appended with a single
`O_APPEND` write. `seq` is monotonic per ledger, starts at 1, and resets after a
prune. `ts` is RFC3339 UTC. `value` is free-form, single-line, non-empty.
Entries are immutable once written, and a torn trailing line from a crash is
dropped with a warning rather than treated as an entry.

The five keys are `goal`, `next`, `done`, `decisions`, `open_questions`. An
unknown key is a clean error.

```text
tk ledger update demo goal "build a codebase-cognizant agent"
tk ledger get demo                  # latest write per key, folded
tk ledger history demo              # every entry, chronological, uncapped
tk ledger prune demo                # human-only, TTY confirmation
```

The write path never truncates, caps, folds, rewrites, or compacts. The
`budgets.ledger_chars` cap (1500) is **retrieval-only**: it trims each value
that `get` returns, on whole runes, with a `...truncated` marker, and it never
touches what is stored. So anchors can never be dropped, and ledger compaction
is resolved by design rather than deferred.

`prune` requires an interactive terminal and refuses off a TTY. There is no MCP
prune tool. Agents append; humans destroy.

`ledger.enabled` (default `true`) is a write-only gate, enforced identically on
both surfaces: `update` refuses with `ledger is disabled (tk config set
ledger.enabled true)`, while `get`, `history`, and `prune` stay open.

## Concurrency, without locks

Safe by construction, deliberately. Appends are single-writer `O_APPEND` lines,
so any interleaving is valid chronology. `get` folds last-write-wins. Reads drop
a torn trailing line. Only single-threaded MCP dispatch writes. No locks, no
SQLite, no fsync-per-append.

## Secret handling, in one place

`memory.SecretMask` over `trace.Redact`, applied before any output reaches a
terminal, an MCP response, or `tk.log`. Fact values that look like secrets queue
for review instead of being stored. Notes are already review-gated. Ledger text
is masked in transit and never appears in an error message. The widened
key-assignment pattern lives here, not in the frozen `trace.Redact` set:

```text
[a-z0-9_-]*(api[_-]?key|secret|token|password|passwd|credential)[a-z0-9_-]*\s*[:=]\s*\S{6,}
```

Redaction doctrine in one line: **inputs broad, outputs narrow.** A secret-shaped
tool parameter is whole-value `[REDACTED]`; output and error text keeps the
narrow, precision-first mask.
