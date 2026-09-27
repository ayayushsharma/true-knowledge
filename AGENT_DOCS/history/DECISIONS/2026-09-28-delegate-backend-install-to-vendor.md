---
title: Delegate backend install to the vendor's own installer script
status: authoritative
date: 2026-09-28
supersedes: null
superseded-by: null
---

# Delegate backend install to the vendor's own installer script

## The gap

`tk install` reimplemented download, checksum verification, and archive
extraction for its managed backends: `internal/installer` grew to 380 lines of
tar, gzip, zip, and sha256 code, plus a 145-line test file, all to put a binary
at `<cache>/bin`.

The first consumer-laptop problem then exposed what that code could not do. A
warm `codebase-memory-mcp` daemon holds the old binary, and on Windows a running
`.exe` cannot replace its own image at all. tk's answer was `os.Rename` over a
live target, which is wrong on both platforms and had no rollback.

CBM already solves this. `codebase-memory-mcp install` stops coordinated
sessions, holds an admission and lifetime barrier exclusively, and swaps the
binary through a transaction that stages beside the target, commits with the old
file retained, validates, and rolls back if validation fails. On Windows a
retained image that cannot be deleted is registered for deletion at reboot
(`CBM_ACTIVATION_TRANSACTION_DEFERRED`). The CBM authors do not self-update for
exactly this reason, and their `update` command is a flag validator that prints
the path to the install script rather than a swap.

The investigation also retired three other worries. CBM commits to additive
schema — `CBM_INDEX_FORMAT_VERSION` is still `1` and `pass_importance.c` says
why: *"no new column, no schema change, and therefore no bump"*, so a store is
never rejected or corrupted across versions, and a genuine format mismatch is
routed through `format_change_reindex` after capturing the ADR store. So no
`_config.db` snapshot is needed.

## Decision

tk stops installing binaries. It runs the vendor's installer script for the
pinned tag and passes `--dir=<cache>/bin --skip-config`, with
`CBM_DOWNLOAD_URL` set to that tag's release base.

The pin survives intact, which was the main objection to delegating: the script
downloads from a tag-scoped base, not `releases/latest`, and verifies SHA-256
against that tag's `checksums.txt`. tk keeps choosing the version; the vendor
does the transfer.

`installer.go` goes from 380 lines to 225, and the deleted 230 are the parts
that were a second implementation of someone else's installer. `backends.Backend`
loses `Checksums` — tk no longer reads a manifest.

## What tk deliberately does not own

* **The bytes.** tk cannot choose which archive lands; it sets the tag and the
  vendor fetches. Trust is the same trust a direct CBM user extends, and the
  checksum is still verified before any write.
* **The swap.** No activation code in tk. The barrier is the vendor's, and
  `install` is the only path that reaches it — the same reason the shell
  installer gets the account-wide guarantee.
* **A rollback store.** Versioned filenames were considered and dropped: the
  transaction already rolls back a failed activation, and the script is
  idempotent, so re-running it at an older tag is the rollback.

## The one thing that needed care

The vendor's `install` appends its bin dir to the user's shell rc. Delegating
without a guard wrote a line into `~/.zshrc` on a real run. tk now passes
`HOME` and `USERPROFILE` pointing at `<state>/install-home`, so that write lands
in a throwaway directory. The install dir is passed explicitly, so nothing the
user needs is lost, and tk still never edits a file it does not own — the rule
`02-BOUNDARY.md` already stated.

The install also runs with the inherited `CBM_CACHE_DIR`, `CBM_RUNTIME_DIR`, and
`CBM_ALLOWED_ROOT` scrubbed and tk's own values appended, so an ambient identity
on a consumer laptop cannot redirect the store or point the drain at an
account-wide cohort. That closes the same class of hole as
`installer.DetectVersion` running `--version` with no environment at all, which
had let a PATH binary answer from a shared cache.

## Boundary note

Delegating means the vendor binary asks coordinated sessions to stop during an
upgrade. That is the same intent as tk's existing rule that only
`install --update` holds an admission barrier: CBM performs its own upgrade
transaction, and tk still never signals a daemon on its own. tk stays fail-open
if the install fails.
