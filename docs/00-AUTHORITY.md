---
title: Docs authority law
status: authoritative
date: 2026-09-26
supersedes: []
superseded-by: null
---

> Authority: this file overrides older docs on conflict.

# 00-AUTHORITY — how truth works here

## tk is pre-release. Nothing below is a compatibility promise.

`tk` is **not** released, not versioned against a stability contract, and has
no users to break. It is pre-1.0 by construction, and this repo treats that as
a licence to fix the design rather than to carry a wrong one.

So, concretely, and stated once here so no individual ADR has to:

* **Machine-readable output has no compatibility guarantee.** `tk <cmd> --json`
  envelopes, `tk mcp` result shapes, and the `--json`/`--budget`/profile flag
  surfaces may change shape, gain or lose keys, or gain and lose sibling keys
  in any release, including patch ones. Code that reads them is code that reads
  an unpinned build.
* **No schema versioning.** There is no `envelope_version`, no `$schema`, and
  no v1/v2 discriminator on any payload. Adding one is a new ADR, and until
  then "the shape changed" is a supported outcome rather than a defect to be
  reported.
* **Adoption is source-level.** A caller that wants a shape to hold still pins
  a commit, and the docs at that commit are the contract. The engine's payload
  is passed through verbatim (structured-payloads ADR), which is *not* a tk
  stability promise: CBM is a separate project on a separate clock, and its
  payload will change under tk's feet. tk does not re-model it, so tk cannot
  promise its contents.
* **What this law does protect:** the *human* CLI surface (rendered tables,
  exit codes, flag names, help text) and the boundary rules in
  `CBM-BOUNDARY.md`. An agent should be able to rely on `tk find` printing a
  table forever, and on tk never touching SQLite — not on the JSON around it.

The one thing this does **not** excuse: silently changing the human surface to
fix a machine-facing problem, or letting the two faces drift. If a change makes
a machine output different, the human output has to stay byte-identical, and
that is enforced by tests (see the dual-path tests in `internal/cli`).

## Precedence

Precedence (highest first):

1. Newest `docs/DECISIONS/YYYY-MM-DD-<slug>.md` with `status: authoritative`
2. `docs/*.md` with `status: authoritative` (`INDEXING`, `CBM-BOUNDARY`, `PATHS-CONFIG`, `AGENT-PROFILES`, `ROADMAP`)
3. `compatible-implementation-spec.md` v1 — **frozen history, powerless on conflict**
4. README examples

Rules:

* Change = new dated ADR + bump affected `docs/*.md` header (`status/date/supersedes`). Never rewrite history.
* Old files get a `superseded-by` stamp only — no content edits.
* Current truth = `grep -r "status: authoritative" docs/`.
* Every `docs/*.md` must carry the YAML header above or CI `docs-lint` fails.
* A doc describing a shape tk no longer emits is **not** to be quietly
  corrected in place unless it is a `docs/*.md` whose header is being bumped by
  the same change. Frozen files and superseded ADRs keep the shape they
  describe; the new ADR carries the correction.
