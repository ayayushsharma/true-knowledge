---
title: find answers a single-token pattern from tk's own index, and names the backend
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# find answers a single-token pattern from tk's own index

## The gap

`tk find` sent every text pattern to the engine's `search_code`: a process
spawn plus a second round trip whenever an empty answer needed the coverage
probe to prove the search had run. The index that already answers
`source_search` in-process could answer `find` too — if its answers meant the
same thing the engine's would.

## The measurement that decided it

First probe, before any design: forward the pattern to Zoekt verbatim. One
case, `retry\.*backoff` against a file containing exactly that line, returned
0 matches. That looked like a structural verdict — Zoekt's parser described in
its own comments as a trigram filter — and produced a literal-only gate.

That gate was wrong, and the probe that killed the verbatim plan was too
narrow to justify it. A second, wider probe — 25 pattern shapes against a real
index on this repo, each compared hit for hit with Go's own regexp — showed the
first failure for what it was: a stale or misread index state, not a filter.
`retry\.*backoff` matches. `.*`, escapes, classes, alternation, groups,
`^ $ \b`, inline `i` and `m` flags, `\Q...\E` — all agree with RE2 exactly,
2–41ms on 232 files. The mechanism is in the code Zoekt ships:
`regexpToMatchTreeRecursive` distills what it can, and `newRegexpMatchTree`
verifies the candidates against RE2 (`index/matchtree.go`). The filter narrows;
the verifier decides.

Four shapes genuinely answer differently, and the differential test pins
each one so a pin bump shows up instead of silently changing a meaning:

* `\z` (end-of-text) is dropped: `backoff\z` finds nothing. Inside an
  alternation the branch vanishes and the other side still answers.
* `\A` (start-of-text) is read as start-of-*line*: `\Aretry` finds 1 of the 3
  lines RE2 finds.
* `(?s)` (dot-matches-newline) degrades matching to a near-match-everything
  filter: `(?s)retry.*backoff` answers 18 of 19 probe lines where RE2 answers
  3 — false positives, the worse direction.
* `(?i)` (case-folding), in every spelling, including `(?-i)`. `(?i)demo`
  searches case-sensitively for the uppercased text, so it misses the `Demo`
  that is there and the `(?i)handler` misses both `Handler` lines; the scoped
  `(?i:MixeD)` diverges the same way. And `(?-i)upper` is ignored, so the
  default fold stays on and over-matches `UPPER` where RE2 says nothing. Any
  inline group naming `i` diverges, however it spells it — which is why the
  gate scans flag letters rather than tracking enable/disable state.

`internal/zoekttext` records both halves: `TestSearchAgreesWithRE2` for the
supported shapes, `TestUnsupportedConstructsAnswerDifferently` for the three
that are not.

## The decision

Two backends, split by what each can answer with the engine's meaning.

* A **single token** that is not a Zoekt operator prefix, not a reserved
  combining word, and contains none of the three divergent constructs is
  answered by tk's own index. Regexes included — metacharacters, anchors,
  classes, flags. No engine process, no installed engine, no second round trip.
* **Everything else** — multi-token phrases (Zoekt ANDs terms per *file*, the
  caller wrote a phrase on one line), operator prefixes (`file:`, `repo:`,
  `case:`, …), bare `or`/`and`/`not`, and the four shapes above — stays on
  the engine's `search_code`, which evaluates the pattern it was given.

`zoektTextQuery` in `internal/cli/query.go` is that gate. The operator list is
copied from Zoekt's own `prefixes` table, not guessed; `mentionsFoldOrDotAll`
refuses any group naming `i` or `s`, because every spelling diverges;
`unquoteAll` keeps `\Q...\E` spans out of both scans (a group name is never a
flag: `(?P<miss>...)` passes).

Every index answer says so: `route: zoekt` in the JSON fields, a `[find: …]`
scope line naming the snapshot on an empty or cut answer, and the freshness
note when the index lags the worktree. A name match (no line) prints as the
name — `file:0:` would invent a location the index does not have.

## What this gives up

* **Case sensitivity differs by route.** The index answers case-insensitively
  by default; the engine's regex is case-sensitive. The index route is a
  superset, so it cannot hide a file that exists.
* **The corpora differ.** The index covers the tracked tree at HEAD, so
  uncommitted work is invisible — disclosed by the freshness note. It can
  report a hit the engine's index would not have held.
* **Latency figures are historical.** 151ms engine vs 28ms index were recorded
  2026-10-03 against CBM 0.11.0; today's regex probes measured 2–41ms index
  side on 232 files with no engine installed. The routing rests on meaning
  equivalence, not on the gap.

## Rejected

* **Forward every pattern including the divergent shapes.** Fast, and wrong in
  both directions: `\z` and `(?i)` lose real matches, `(?s)` and `(?-i)` invent them.
* **Trust the index on hits, fall back to the engine on empty.** The index can
  miss files the engine holds, so the result set would be engine-dependent in a
  way no single note discloses.
* **Make `grep` use the same gate.** Same fix is available; deferred, needs its
  own decision.

## Where it lives

`internal/cli/query.go` (`zoektTextQuery`, `mentionsFoldOrDotAll`, `unquoteAll`,
`findText`, `findTextScope`), `internal/mcp/zoekt.go` (`ZoektQuery`,
`writeMatch`), `internal/zoekttext` (equivalence + divergence tests),
`internal/cli/find_test.go`, and three e2e cases that count engine processes:
`find-regex-uses-no-engine`, `find-filename-uses-no-engine`, and
`find-phrase-stays-on-the-engine`.
