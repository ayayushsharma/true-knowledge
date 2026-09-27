---
title: A real-repo end-to-end gate — fake engines cannot see scale
status: authoritative
date: 2026-09-27
supersedes: null
superseded-by: null
---

# A real-repo end-to-end gate

## The gap

Every test that exercised a CBM spawn used a shell fake: a script that matched on
the tool name and printed counters. That fake is deliberate and correct for CI —
it is fast, hermetic, and it pins output *shape*. It also cannot see anything that
only appears at scale, because a three-line fixture has no shard boundaries, no
partial-coverage notices, no result caps, and one language.

So the suite was green against a system that had never indexed a repository
anybody works in. Real code was untested.

## Decision

`tests/e2ereal` drives the built binary against a real `codebase-memory-mcp` and
a real repository, behind two gates so it can never slow the default suite: the
`e2ereal` build tag and `TK_E2E_REAL=1`. `TK_E2E_REPO` names the repository; the
test registers it, indexes it, and asserts nine properties.

```bash
mise run e2e-real REPO=/home/ayush/projects/personal/tensorflow
TK_E2E_REAL=1 TK_E2E_REPO=/path/to/repo go test -tags e2ereal -timeout 90m -v ./tests/e2ereal/
```

The test installs CBM itself with `tk install cbm` and asserts the installed
version equals `cbm_version_pin`, so the gate also covers the installer's
checksum path rather than trusting a binary someone left on the machine.
`TK_E2E_CBM_BIN` reuses one for offline runs.

### The probe is read from the repository, not hardcoded

The test walks the checkout for a definition line, per language, and takes the
first symbol the graph confirms. A hardcoded symbol list would rot the first time
a file was renamed in the target repository, and the failure would look like a tk
regression. Reading the probe out of the repo makes the test reproducible against
any checkout: point it at a Go repo and it probes a `func`, point it at
TensorFlow and it probes a `def` in `ci/official/utilities/`.

## What it asserts, and why each one

* The registry reports the indexed repo's own HEAD, `state: clean`, and a
  `zoekt_head` equal to HEAD. A text index behind the graph is a stale answer
  waiting to be served.
* The graph knows a symbol read out of the repo, so the engine parsed a language
  that is not tk's own.
* `find --json` carries a `qn_rule`, and the qualified name rebuilt from
  `qn_prefix + name` is exactly what `explain` resolves, with non-empty source
  and a path under the repository. This is the chain every agent performs by
  hand; a renderer that changes the rule breaks all of them silently.
* `validate` on a near-miss name returns the coverage verdict —
  `generation_matches`, `hash_records_complete`, `recording_status` — so a
  negative claim is never a bare miss.
* `explain` on an unresolvable name exits non-zero with empty stdout and a
  recovery hint, on both the human and `--json` faces. Exit 0 plus empty stdout
  is indistinguishable from a crash.
* `source-search` names only files that exist in the worktree, and its envelope
  reports `zoekt_fresh` plus the unindexed worktree counts.
* `sync` on a clean HEAD is a no-op.
* MCP `tools/list` returns the eleven documented scout tools by name, not by
  count, and `search_graph` answers with a `structuredContent` payload.
* The trace log is JSONL with a populated `argv` on every entry.

## What the first run found

Nine assertions, three failures, and **all three were bugs in the new test**, not
in tk. Worth recording, because the tempting conclusion — "real CBM found three
defects" — was wrong, and the way it was wrong is the useful part.

* `zoekt_head` read as empty. The anonymous struct decoding `status --json` had
  no `json:"zoekt_head"` tag, and Go's case-insensitive field matching does not
  ignore underscores. A missing tag looked exactly like a freshness bug.
* `near_miss` was required on a name with no similar symbol. The coverage verdict
  — the actual rule — was present and correct; near misses are best-effort and
  legitimately absent. Asserting them made a working surface look broken.
* `source-search` was asked to find a literal in a file it had indexed, and
  reported nothing. It had returned its first `--limit 20` rows, and the file
  was not among them. The cap is explicit and documented; the assumption was
  not.

The two real properties of the system that the chase exposed, both correct
behavior worth knowing: `tk index` records `zoekt_head` when the process exits,
so polling `status` during a long index shows the project as `dirty` with an
empty text head — which is the truth, not a gap; and `source-search` caps at 20
rows with no in-band marker, so a caller that needs more must pass `--limit`.

## Cost and what stays out of CI

A full run against a 2.0 GB TensorFlow checkout is 187 seconds: about 10 s to
install CBM, 150 s to index 20,455 files at `moderate`, and the rest assertions.
That is too slow for every commit and fast enough to run before a release or
whenever the engine is upgraded. The fake-backend suite stays the default; the
real gate is the one that runs when a CBM pin changes.
