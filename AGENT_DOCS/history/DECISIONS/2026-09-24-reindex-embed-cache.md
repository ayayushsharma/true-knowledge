---
title: Reindex embedding ceiling — persistent content-keyed cache + chunked warm embeds
status: authoritative
date: 2026-09-24
supersedes: [docs/DECISIONS/2026-09-23-mvp3-memory-layer.md §Embeddings (reindex embed mechanics)]
superseded-by: null
---

# ADR — reindex embedding ceiling: persistent content-keyed cache, chunked + warm embeds

## Context

MVP3 shipped embeddings on `tk note reindex` as **one** `Embed(ctx, bodies)` call
over the whole corpus before a single insert transaction, then dropped the note
tables (`DROP TABLE notes`, `DROP TABLE notes_fts`) and rebuilt from the
markdown. Two failure modes fell out:

1. **Cold-model ceiling.** The default `embedding.timeout_ms` is 3000ms, but a
   cold Ollama model load takes 10-30s. The single big call consumed the whole
   corpus in one `http.Client` timeout. When it failed, *every* note stayed
   keyword-only — an all-or-nothing silence with no partial credit, and no way
   for a slow-but-healthy model to ever warm up.
2. **No incremental path.** Because the index tables are dropped, nothing
   survives a rebuild. `tk note reindex` re-embeds the entire corpus every
   time, even when a single note changed, and vectors evaporate whenever the
   endpoint is briefly unavailable during a rebuild.

Notes are markdown-first (`notes/<project>/*.md` is the truth; the index is
derived). Total-rebuild is intentional there — but it must be cheap and
resilient, not a fresh all-or-nothing network gamble each run.

## Decision

### Schema v3: persistent embed cache `note_embeds` (never dropped by reindex)

```sql
CREATE TABLE IF NOT EXISTS note_embeds (
    file      TEXT NOT NULL,   -- <project>/<file>.md relative to notes root
    model     TEXT NOT NULL,
    digest    TEXT NOT NULL,   -- sha256 hex of the note body
    embedding BLOB NOT NULL,
    PRIMARY KEY (file, model)
);
CREATE INDEX IF NOT EXISTS note_embeds_model_idx ON note_embeds (model);
```

`Reindex` touches `notes_fts`/`notes` only. `note_embeds` is a separate,
content-keyed cache that survives table rebuilds. Migration simplifies into
stepwise idempotence: run the v1→v2 column ALTER when needed, then always
execute `notesSchema` (all `CREATE … IF NOT EXISTS`) and set `user_version = 3`.

### Reindex embed path: reuse → warm → chunk, all fail-open

Per `Reindex`, when an embedder is configured and notes exist:

1. Load `note_embeds` rows for the current model; any entry whose body digest
   matches its cached digest reuses the stored vector — **zero HTTP**.
2. Remaining (new/changed) bodies are embedded in chunks of `embedChunkSize`
   (64) after a single **warm call** with a throwaway constant body. The warm
   call forces a cold model load under the configured timeout exactly once; a
   warm failure (endpoint down, missing model, load slower than timeout) means
   chunking would just repeat it, so we skip to BM25-only rather than pay
   `chunks × timeout`.
3. A per-chunk failure degrades **only that chunk** to keyword-only and
   continues — partial credit by construction.
4. In the same insert transaction as the note rebuild, the cache is refreshed
   for the current model: rows whose file vanished are pruned, everything else
   is `INSERT OR REPLACE` re-keyed by body digest.

Embedding HTTP stays outside the transaction (unchanged invariant).

## Consequence

* **Reindex is cheap for stable corpora**: unchanged notes hit the cache, a
  full rebuild after a single edit re-embeds exactly the edited note.
* **Cold models converge**: with an adequate `timeout_ms`, a warm call absorbs
  the model load and the first chunk lands real vectors; the default 3000ms
  still fails open to BM25 with the hint to raise `embedding.timeout_ms`.
* **Rebuilds survive endpoint blips**: vectors persist in `note_embeds`, so an
  unchanged reindex against a dead endpoint keeps its vectors and search.
* Config version stays 1 (no new keys); notes index `user_version` 2→3.
* Tests: `TestNotesReindexIncrementalCache` (dead-endpoint rebuild keeps
  vectors), `TestNotesReindexChunked` (warm + ⌈n/64⌉ chunk calls),
  `TestNotesReindexChunkFailOpen` (one bad chunk degrades it alone).
* e2e: new skip-guarded live-Ollama block asserts `live-ollama-vectors-stored`
  and `live-ollama-cache-survives` against a real `/api/embed` daemon.