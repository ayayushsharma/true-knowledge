---
title: Frozen v1 spec joins history — the repo root holds no prose
status: authoritative
date: 2026-09-27
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-27-consolidated-agent-docs.md (the "What did not move" clause only)]
superseded-by: null
---

# Frozen v1 spec joins history

## What changed

`compatible-implementation-spec.md` moved from the repo root to
`AGENT_DOCS/history/compatible-implementation-spec.md`, byte-identical, verified
with `git show HEAD:compatible-implementation-spec.md | cmp` before staging. The
same commit repointed the six authored documents and `manifest.json` that cite
it, and `00-INDEX.md` now names it as the lowest precedence entry it always was.

## Why the earlier call was wrong

The consolidation ADR left it at the root on the grounds that every ADR already
cited it there. That reasoning optimised for not touching a citation count and
missed the actual problem: a 1,116-line prose file at the repo root reads as
current, and this one is the single most dangerous document in the repo to
misread.

It is not merely outdated, it is contradicted. Its STATUS line says not to
implement §5.2, §6, §12, §13, and §15 as written, because thin tk delegates the
daemon, the store, and the watcher to CBM. An agent that opens the root file,
reads it as a specification, and implements §5.2 writes a daemon that tk's
boundary forbids. `02-BOUNDARY.md` is the opposite of what §5.2 says, and the
whole point of the precedence chain is that the lower-numbered file wins. Root
placement gave the losing document the most visible address in the repository.

The consolidation pass had already noticed the contradiction and filed it as a
permanently rejected item in `08-BACKLOG.md`; leaving the file in place meant
the backlog entry and the file both had to be right for the outcome to be
safe. Moving it makes the location itself carry the verdict.

## Why the filename did not change

`history/compatible-implementation-spec.md` keeps its name. Eleven frozen files
cite the bare filename, and a rename would break every one of them permanently,
since history is immutable and cannot be repointed. Keeping the stem means a
stale citation is a missing directory prefix, which is one obvious hop away from
`00-INDEX.md`, rather than a vanished file with no way back.

Those eleven citations are now stale and will stay that way, which is the
documented cost of immutability rather than an oversight. `00-INDEX.md` is the
live map from the pre-consolidation names to their current homes.

## The spec's own STATUS line was left alone

It still names `docs/00-AUTHORITY.md`, `docs/CBM-BOUNDARY.md`, and four more
paths that moved a week ago. Editing one line would have made it navigable, at
the price of a frozen file no longer being frozen — and the first exception is
how "we only ever changed the header" starts.

A live pointer in a frozen document is a liability, so `00-INDEX.md` carries the
mapping instead: one authoritative file, no exceptions, and the rule stays
absolute. Reversing this is a two-line edit if the cost of the dead links
outweighs it.

## Enforcement

`internal/docs` gained `checkSpecPlacement`. The spec must exist at
`AGENT_DOCS/history/compatible-implementation-spec.md` and must not exist at the
repo root, so the file cannot drift back to the visible address by accident or by
an agent assuming a root-level `.md` is live.

The check is deliberately not part of the forbidden-path scan. A substring rule
for the bare filename would also match the correct path inside `AGENT_DOCS/`,
since one is a suffix of the other; asserting both homes directly is exact.

## Compatibility

None. No code, no CLI, no MCP surface, and no config key is affected. The file
is a reference document that no program reads, and `cmd/genman` is unaffected
because it only writes `docs/man/`.
