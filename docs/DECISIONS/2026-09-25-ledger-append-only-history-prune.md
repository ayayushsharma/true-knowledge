---
title: Ledger v2 — append-only, five keys, retrieval-only budget, human-only prune (drops ref field)
status: authoritative
date: 2026-09-25
supersedes: [docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (ledger storage + budgets.ledger_chars semantics), docs/REMAINING-WORK.md §4 (ledger compaction deferred item)]
superseded-by: null
---

# ADR — Ledger v2 spec

## Context

The v1 ledger is `ledger/<project>.json`: a single bounded blob per project,
where `Update` is a last-write-wins full-text replace truncated to
`budgets.ledger_chars` (`internal/memory/ledger.go:105-131`). Two defects:

* **Tail-truncation can silently drop durable facts.** A later update's budget
  trim can evict an "anchor" the earlier ledger stated, so subsequent updates
  build on stale working-truth. This is the compaction/re-anchor problem
  (REMAINING-WORK §4).
* The write path destroys information to save context — the budget exists to
  bound a context window, not the disk.

The ledger's consumers are agents (MCP `memory`-profile tools); humans rarely
touch it. Requirements that follow: durable fact growth with auditable
evolution, no LLM rewrite/compaction, deterministic surfaces, and deletion
that humans alone control.

## Decisions

1. **Append-only JSONL (v2).** `ledger/<project>.jsonl`, one entry per line,
   `O_APPEND` single atomic write, 0600. Entries are immutable once written.
2. **Write path = verbatim append.** No cap, no fold, no interpretation on
   write. Validation only: key must be one of the fixed five, value non-empty.
   The write-time truncation in `ledger.go:111-118` is removed.
3. **Keys: the fixed five, last-write-wins fold.** Entries are tagged
   `goal | next | done | decisions | open_questions`; `get` folds the latest
   entry per key. No collections, no cardinality machinery — the keys explain
   the project's direction/state, and that is enough. Unknown key = clean error
   (keeps the `get` shape deterministic).
4. **Retrieval-only budget.** `budgets.ledger_chars` (default 1500) caps each
   `value` returned by `get`: whole runes + `...truncated` marker. The cap
   never touches stored entries.
5. **`history` is always a complete return.** Every entry, chronological, no
   flags, no filters, values uncapped.
6. **`prune` is human-only.** CLI `tk ledger prune <project>` requires an
   interactive TTY confirmation and refuses to run off-TTY (`--yes` not honored
   off a terminal). There is **no MCP prune tool**. Agents append; humans
   destroy.
7. **MCP `memory` profile 20 → 22.** New tools `ledger_get` and
   `ledger_history`; `ledger_update` gains a `key` param and append semantics.
   CLI mirrors the same surface for debugging/ops.
8. **No `ref` field.** The envelope is `{seq, ts, key, value}`. History IS the
   audit trail; any evidence an agent needs lives inside `value` text. A
   dedicated reference field adds surface without adding capability.

## Spec (normative)

### Storage format

* Path `<data>/ledger/<project>.jsonl` (dirs 0700, files 0600).
* One JSON object per line:
  `{"seq":1,"ts":"2026-09-25T10:04:00Z","key":"done","value":"shipped picker + man pages"}`
* `seq`: monotonic per ledger, starts at 1, resets after `prune`.
  `ts`: RFC3339 UTC. `key`: one of the fixed five. `value`: free-form
  single-line string (no embedded newlines).
* Append = single `O_APPEND` write of the line + `\n`. A trailing partial line
  (crash) is tolerated: dropped with a warning, never treated as an entry.

### Semantic contract

* **update** (CLI/MCP): validate key ∈ the five and non-empty value → append
  `{seq, ts, key, value}`. Never truncates, never rewrites prior entries.
* **get**: fold the latest entry per key → dict keyed by the five keys
  (empty ledger → empty dict, not an error). Each `value` capped to
  `budgets.ledger_chars` by whole runes + `...truncated`. Each key carries its
  `as_of` seq/ts so the caller sees freshness.
* **history**: all entries in append order, complete, uncapped.
* **prune**: delete the project's ledger file(s) after interactive TTY
  confirmation; refuses off-TTY. `--all` scope stays a later decision (not
  built).

### Surfaces

* MCP `memory` profile (20 → 22):
  `ledger_update {project, key, text}` · `ledger_get {project}` ·
  `ledger_history {project}`. No prune tool.
* CLI:
  `tk ledger update <project> <key> <value>` · `tk ledger get <project>` ·
  `tk ledger history <project>` · `tk ledger prune <project>` (TTY-confirm).

## Consequence

* Config: `budgets.ledger_chars` meaning changes to "retrieval-only cap"
  (name unchanged). `ledger.enabled` unchanged. Config version stays 1: the
  key already exists, only its semantics move.
* Code: rewrite `internal/memory/ledger.go` (+ tests); `internal/cli/ledger.go`
  (update/get/history/prune); `internal/mcp/server.go` tool defs +
  `internal/mcp/memory.go` dispatch; bump pinned `memory(20)` → `memory(22)`
  in `internal/cli/mcp.go` help + `docs/man/tk_mcp.1` (regen 49 pages via the
  `docs.man` task); completion; `server_test.go` pins; e2e additions.
* Docs: mvp3 ADR ledger lines superseded (stamped); PATHS-CONFIG tree + key
  description; ROADMAP MVP3 bullet + deferred list (compaction resolved);
  REMAINING-WORK §4 shrinks to RRF tuning only; AGENTS.md `memory(20)` →
  `memory(22)`.
* Migration: legacy `ledger/<project>.json` blobs are ignored by the v2
  reader; the first update for a project starts a fresh `.jsonl`; `prune`
  removes both shapes. No migration pass (no deployed users; value loss on
  rewrite is acceptable).
* Done-when: an agent can update (append) → get (folded winners, capped) →
  history (complete) → human `prune`; nothing on the write path truncates or
  rewrites; `budgets.ledger_chars` truncation appears only in `get` output.