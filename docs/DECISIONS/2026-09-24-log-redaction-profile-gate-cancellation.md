---
title: Log redaction, MCP profile gate, zoekt cancellation (hardening pass)
status: authoritative
date: 2026-09-24
supersedes: [docs/DECISIONS/2026-09-23-mvp3-memory-layer.md (trace.Redact-only redaction doctrine), docs/DECISIONS/2026-09-24-zoekt-staleness.md (source_search serving; this ADR gates + cancels it)]
superseded-by: null
---

# ADR — secrets in logs, hidden-tool gate, cancellable zoekt

## Context

Three findings from the `comments.md` review pass, each a real (not cosmetic)
gap:

1. **MCP logs leaked raw args.** `tk mcp`'s per-call `tk.log` record wrote the
   tool `params` verbatim, so a `mem_save`/`note_save`/`ledger_update` value or
   a search pattern that *looked* secret landed on disk unmasked. The memory
   layer correctly routes secret-shaped values to a review queue, but the log
   pre-dated that protection. CLI `finalize` masked argv + output + error but
   not backend `events` (Detail/Error could echo a query pattern).
2. **Profile enforcement skipped `source_search`.** `callTool` dispatched
   `source_search` before the profile gate, so minimal (3 tools) could be made
   to run a tool it never advertises — "hidden" only by advertisement, not by
   enforcement. Every other tool was already gated.
3. **zoekt ignored the caller context.** Search built its 60s window from
   `context.Background()`, so an MCP client disconnect or Ctrl-C could not
   stop a long query, and a cancelled caller was ignored during indexing.

## Decision

* **Logs: inputs broad, outputs narrow.** String tool params get the CLI-argv
  policy — whole-mask `[REDACTED]` when `memory.DetectSecret` flags the value
  (covers composites like `API_SECRET_KEY = v` via a widened key-assignment
  pattern), span-mask otherwise. Output/error text stays on the narrow
  `trace.Redact` set: precision first, legit code-search lines survive
  (documented `testRedactLeavesCodeAlone` doctrine). CLI `finalize` now masks
  backend event Detail/Error too. `trace.Redact` itself is unchanged — the
  widened `[a-z0-9_-]*(api[_-]?key|secret|token|password|passwd|credential)
  [a-z0-9_-]*\s*[:=]\s*\S{6,}` assignment pattern lives in the memory layer,
  where flagging more aggressively is safe (values pre-route to review).
* **Profile gate is first.** `hasTool(name)` runs before any dispatch,
  including the source_search special case — tools hidden from a profile are
  `-32601 unknown tool` even when implemented. Both the `tools/list`
  advertisement and `tools/call` enforcement now agree.
* **Every zoekt entry point takes `ctx`.** `Search`/`SearchLive` derive their
  60s cap from the caller context (`context.WithTimeout(ctx, ...)` — whichever
  bound fires first wins), so disconnect/deadline cancellation propagates
  instantly. `IndexDir` honors ctx between files in its walk. `IndexGitRepo`
  exposes no ctx at the pinned rev; `IndexRepo` honors it before/after the
  build and documents the mid-build window (bounded by the build itself, ≤ the
  full normal rebuild ~19s) as a future-pin upgrade.

## Consequence

* A leaked secret in tk.log is now a bug with a test (`TestMCPLogSecretRedaction`,
  `TestFinalizeMasksEventSecrets`), not a review finding. `API_SECRET_KEY = v`
  style composites route to review and mask in logs where the old pattern
  missed them.
* minimal's 3-tool surface is hermetic: hidden tools cannot be invoked
  (`TestSourceSearchHiddenInMinimal`).
* Long/abandoned queries stop when the caller stops — no 55s zombie searches on
  client disconnect (`TestCancellationHonored`).
* Memory review routing now flags slightly more values (composite key names);
  the widened pattern never touches `trace.Redact`, so search *output* stays
  precision-clean.