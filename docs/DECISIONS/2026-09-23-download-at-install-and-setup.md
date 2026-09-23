---
title: Download-at-install default plus tk setup
status: authoritative
date: 2026-09-23
supersedes: []
superseded-by: null
---

# ADR — download-at-install default, `tk setup` as the front door

## Context

Two ways to get backend binaries onto a machine: bundle them inside tk's
release archives, or download pinned releases at install time. Bundling
couples tk's release matrix to every backend's (5 CBM platform archives today,
more backends tomorrow) and bloats every install.

## Decision

* **Download-at-install is the default.** `tk setup` / `tk install` fetch
  pinned, checksum-verified assets on demand. Small tk releases, independent
  backend versioning (`tk install <backend> --update` without a tk release).
* **Bundled sidecar stays supported, not default:** a `codebase-memory-mcp`
  (or zoekt) binary sitting next to `tk` is found via the sibling lookup and
  preferred — release archives *may* ship both, airgap users *can* assemble
  that layout by hand.
* **Never:** `go:embed` multi-hundred-MB executables inside `tk`, or shell
  out to system package managers at runtime.
* **`tk setup` is the front door:** `init` (idempotent) → `install` all
  backends at pins → opt-in `--register` (cwd, never default) → opt-in
  `--client` snippet. Granular `init`/`install` remain for scripting.
* **Provenance rule:** bytes are never rebuilt or stripped by tk. Trust comes
  from manifest verification at package time (bundled) or install time
  (downloaded) — mirroring CBM's own release policy.

## Consequences

* No release-matrix coupling for MVP. Sidecar layout works today with zero
  extra code (resolver order covers it).
* Fresh-machine story is one command with one failure mode (network), and
  `--dry-run` previews every URL before touching the network.
