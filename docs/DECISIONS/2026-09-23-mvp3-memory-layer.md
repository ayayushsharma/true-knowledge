---
title: tk-owned memory layer (facts/notes/ledger) on SQLite + optional embedding endpoint
status: authoritative
date: 2026-09-23
supersedes: [compatible-implementation-spec.md §7 (memory stores), §8 (mem_*/note_*/ledger_* tool matrix)]
superseded-by: null
---

# ADR — tk-owned memory layer: SQLite storage, endpoint embeddings, memory MCP profile

## Context

The v1 spec sketched `mem_*`, `note_*`, and `ledger_*` stores and a daemon-based
tool bus. MVP2 confirmed CBM is graph/source-index only; its bundled
`nomic-embedding-code` serves **code** semantic queries and there is **no
general arbitrary-text embed tool** on CBM's surface. Long-term memory
(facts, notes, project working-truth) has to be tk-owned. Three decisions were
forced: how to store (SQLite vs JSON files), how to do semantic notes search
(bundle a model vs call an endpoint), and where the memory tools live on the
MCP surface.

## Decision

### Storage: SQLite (CGo-free wrapper), markdown as notes truth

* `mem/facts.db` holds facts + review queue; `notes/index.db` holds the FTS5
  BM25 index; ledger stays bounded JSON (`ledger/<project>.json`) per the
  frozen spec.
* Driver is `modernc.org/sqlite` pinned in `go.mod` (`modernc.org/libc`
  pinned to match). No CGo, released in the single static `tk` binary
  (~15–20MB growth, accepted). FTS5 is built in.
* Notes are **durably written as markdown** under `notes/<project>/*.md`
  (front-matter JSON + body); the index is derived and fully rebuildable
  (`tk note reindex`). The DB is never the truth for notes.
* **FTS5 must not use external-content tables**: modernc's FTS5 raised
  `database disk image is malformed (267)` on first insert. Use a standalone
  FTS5 table keyed by the notes table's `rowid`.
* Schema-versioned (`PRAGMA user_version`), WAL, 0600 files, per-store dirs
  under `~/.local/share/true-knowledge/`.

### Embeddings: external endpoint only, BM25 stays authoritative

* **tk never bundles or downloads embedding models.** Semantic enrichment
  talks to any Ollama-compatible `POST /api/embed` endpoint configured under
  `embedding{enabled,endpoint,model,timeout_ms}` (default off,
  `http://127.0.0.1:11434`, 3000ms).
* `approve`/`reindex` attach an embedding + model tag when enabled. Search is
  BM25 (FTS5) **fused via reciprocal-rank fusion** with a cosine pass over
  stored embeddings.
* Fail-open by contract: endpoint down, timeout, HTTP error, model mismatch,
  or an index carrying no matching-model vectors ⇒ plain BM25. Search never
  fails because embeddings are missing.

### Secret policy: none of it ever lands unvetted

* Secret-shaped values are masked (`memory.SecretMask` layered over
  `trace.Redact`) before CLI output, MCP tool output, or any `tk.log` write.
* Secret-looking values (API keys, tokens, JWT, private keys, high-entropy
  strings) are never stored silently: facts go to the review queue; notes are
  already review-gated; ledger text is masked in transit, never in errors.

### MCP surface: a `memory` profile, in-process and CBM-free

* `memory` = scout (11) + 9 tk-owned tools: `mem_save`, `mem_recall`,
  `mem_review`, `note_save`, `note_search`, `note_toc`, `note_reindex`,
  `note_review`, `ledger_update` (20 total).
* The memory tools run **inside the tk stdio server** — no spawn, no CBM, no
  daemon — so long-term memory keeps working when graph/code backends are
  missing or down.

## Consequence

* CLI: `tk mem|note|ledger` commands cover the same surface offline.
* Config keys added: `embedding.*`, `ledger.enabled`, `budgets.ledger_chars`,
  `budgets.notes_toc_chars` (a `notes_toc` budget added earlier). `version`
  stayed 1: optional keys decode into defaults, so existing configs load.
* e2e covers both modes (fake + live CBM), including a live `/api/embed` mock
  with down-fallback and masked-secret checks.