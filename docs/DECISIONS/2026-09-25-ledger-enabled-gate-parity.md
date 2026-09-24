---
title: ledger.enabled gate enforced on MCP writes — parity with the CLI
status: authoritative
date: 2026-09-25
supersedes: []
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — ledger.enabled write gate applies to MCP too

## Context

Bug: `ledger.enabled` was enforced differently on the two surfaces.

* **CLI** — `tk ledger update` refuses to append when `ledger.enabled false`
  (`internal/cli/ledger.go`, write gate only; `get`/`history`/`prune` stay
  open).
* **MCP** — `ledger_update` ignored the flag entirely: `memoryStore`
  (`internal/cli/mcp.go`) built the `memory.Store` unconditionally and
  `Store.LedgerAppend` had no gate, so an agent appending through the `memory`
  profile always succeeded while the same write from a human was blocked by
  the console CLI. The e2e covered the CLI gate but not the MCP path.

The gate is a **write-only** gate (mirrors `budgets.ledger_chars`; reads are
harmless and stay open). The write path is the only place the flag was ever
meant to bite, so parity means gating `ledger_update` exactly like
`ledger update`, with the same message.

## Decision

Gate the write path in the one shared backend:

1. `memory.Store` gains `LedgerEnabled bool` (explicit, config-decoupled like
   `LedgerBudget`); `Store.LedgerAppend` returns `memory.ErrLedgerDisabled`
   when false. Reads (`LedgerGet`/`LedgerHistory`) are untouched.
2. `internal/cli/mcp.go` wires `LedgerEnabled: c.Cfg.Ledger.Enabled`.
3. CLI reuses `memory.ErrLedgerDisabled` so both surfaces emit the identical
   text: `ledger is disabled (tk config set ledger.enabled true)`.
4. Default config keeps `ledger.enabled: true`; behavior is unchanged for
   everyone who never flipped the flag.

## Consequences

* `ledger_update` now fails with the error envelope iff `ledger.enabled false`
  in `tk config` — same rule the CLI already applied. `ledger_get` /
  `ledger_history` remain available either way.
* New regression tests: `TestLedgerEnabledGateMatchesCLI` (MCP store with the
  gate off → append errors with the shared message, `get` stays open) plus the
  e2e `mcp-ledger-disabled-gate` while the flag is false.
* No config change, no new keys, no completion/`--help`/man-page impact, no
  profile-count change (ledger tools exist only in `memory(22)`).
* Un-parking/triggers: none — this is a consistency fix to documented
  behavior, not a new decision surface.