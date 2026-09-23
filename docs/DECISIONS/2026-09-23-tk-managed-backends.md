---
title: tk installs all its own dependencies
status: authoritative
date: 2026-09-23
supersedes: []
superseded-by: null
---

# ADR — tk-managed dependencies (no system package managers)

## Context

`tk` drives external code-intel binaries but initially assumed they appear on
`PATH` by magic. System package managers drift, lag, or are absent on fresh
machines — and tk must work identically on Linux/macOS/Windows without them.

## Decision

* `tk` is its own package manager for backends: `tk install` / `tk setup`
  download pinned releases into `<cache>/bin`, verify SHA-256 manifests
  **before any write**, install atomically (tmp + rename), and record pins in
  `.tk-versions.json`. Checksum mismatch aborts with zero partial state.
* Pins are the only version truth: `config backends.<name>` (`cbm: 0.11.0`,
  `zoekt: <full commit SHA>`). X.Y.Z or 7–40 hex chars validate; anything else
  is rejected at `config set` time.
* Resolver order: `TK_<NAME>_BIN` > bundled sibling of `tk` > `<cache>/bin` >
  `PATH` > download. The managed copy wins over PATH so updates take effect.
* Adding a backend = one entry in `internal/backends` (name, repo, binaries,
  asset naming, tag scheme). Installer, status, and `tk install` are generic
  over the registry — no per-backend code paths.
* Per-backend mirrors via `TK_RELEASE_BASE_URL_<NAME>`, global fallback via
  `TK_RELEASE_BASE_URL` (tests, airgap mirrors).

## Consequences

* Fresh machine: `tk setup` alone reaches working graph+text search. No
  apt/brew/npm/toolchain at runtime.
* tk owns download+verify code (stdlib only) and a release job that builds
  source-only backends (zoekt) per platform.
* `tk install --check/--dry-run/--update` report, preview, and refresh pins.
