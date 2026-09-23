---
title: Docs authority law
status: authoritative
date: 2026-09-22
supersedes: []
superseded-by: null
---

> Authority: this file overrides older docs on conflict.

# 00-AUTHORITY — how truth works here

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
