---
id: 2026-10-11-resident-drops-empty-string-args
title: Resident drops empty-string args so both paths ask the engine the identical question
status: authoritative
date: 2026-10-11
supersedes: []
superseded-by: null
---

# Resident drops empty-string args so both paths ask the identical question

`tk grep` always sends `file_pattern` ("" when `--files` is unset). The spawn
path drops it in `writeArgs`; the resident forwarded it verbatim, and the
engine reads a present `""` as a real filter — `fnmatch("", basename)`
excludes every indexed file, so `search_code` answers empty. Result: `tk
grep` returned 3 results via spawn and 0 via a running resident, for the same
project, pattern, binary, and on-disk index. Coverage passed on both paths,
which made the divergence look like daemon state; bisecting over MCP
(`file_pattern` absent → 3, present-`""` → 0, `scan_ms: 0` vs `109`) proved
it was argument shape.

`internal/resident/server.go` now cleans args with `dropEmptyStrings` where
they are finalized, before both the plain and `withFormat` branches — the
same rule `writeArgs` documents, at the point the code already promises both
paths "ask the engine the identical question". `TestResidentDropsEmptyStringArgs`
pins it for both call shapes. Verified on-device: `tk grep` via a detached
resident returns the same 3 results as the spawn.

Upstream note (not patched): treating present-empty as "match nothing" is
defensible but sharp; the unscoped `find -name ''` branch has the same trap.
If upstream ever normalizes `""` to absent, tk's cleaning stays harmless.
