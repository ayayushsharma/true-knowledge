---
title: Delivery parked — package-manager + .zst cadence deferred; download scripts later
status: authoritative
date: 2026-09-25
supersedes: [docs/REMAINING-WORK.md §2 (packaging + team .zst cadence sub-items), docs/ROADMAP.md Ops line (packaging / .zst cadence half)]
superseded-by: null
---

> Authority: this file overrides older docs on conflict. See docs/00-AUTHORITY.md.

# ADR — park brew/nix/npm packaging and the team `.zst` cadence

## Context

Distribution today is `mise run build` → `dist/tk` (static binary, local
build; `.gitignore`d intent). REMAINING-WORK §2 promised two delivery
sub-items: **package-manager formulas** (brew/nix/npm) and a **team `.zst`
ship cadence** (committed `graph.db.zst` artifact distribution schedule with
age/last-built surfacing in `status`).

Both are disproportionate before there is demand: formula upkeep is a
per-ecosystem maintenance + review surface for a single static binary that a
download URL already serves; the `.zst` cadence adds a commit/ship schedule
for artifacts CBM already writes (INDEXING.md:57) and nobody consumes yet.

## Decision

* **Park indefinitely:** package-manager delivery (`brew`/`nix`/`npm`
  formulas) and the team `.zst` distribution cadence. The `.zst` artifacts
  themselves are unchanged — still CBM-owned, still written as today; only
  tk's *distribution cadence* for them is parked. Revisit only on a concrete
  trigger: a team actually requiring a package manager, or verified consumer
  demand for `.zst` age surfacing.
* **Recorded direction — deliberately NOT built now, decided later:** delivery
  via the existing GitHub release page (`ayayushsharma/true-knowledge`):
  platform binaries + `checksums.txt` fetched by simple **`install.sh`**
  (curl, sha256sum) and **`install.ps1`** (PowerShell `Invoke-WebRequest`,
  `Get-FileHash`) download scripts, latest-or-pinned tag. Writing these
  scripts and the release workflow is future work, parked with this ADR.

## Consequences

* REMAINING-WORK §2 drops to three independent ops items: `trajectory.ndjson`,
  resource-limit surfacing, and (kept on record) the deferred download-script
  delivery path.
* ROADMAP Ops line updated; local build path (`mise run build`) unchanged.
* `tk install` (the CBM installer) is unrelated to this — it stays as-is.