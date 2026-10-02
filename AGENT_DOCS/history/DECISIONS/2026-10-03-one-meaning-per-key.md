---
title: One meaning per key — status --json carries the same freshness fields as every read command
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# One meaning per key

## The gap

`Ctx.freshness()` is the single source of the freshness fields. Ten read
commands reach it through `outFresh`/`outDataFresh`, and it reports a pair:

```
head        the RECORDED commit (p.Head), or the recorded fingerprint
current     the LIVE commit (gitx.Head), or the live fingerprint
fresh       head == current
```

`cmdStatus` did not use it. It built its own row map and put the **live** head
in `head`:

```go
head := gitx.Head(p.Path)          // live
rows = append(rows, map[string]any{..., "head": head, "zoekt_head": p.ZoektHead, ...})
```

So one key name carried two meanings on one surface:

| command | `head` meant |
|---|---|
| `find`, `explain`, `trace`, `grep`, `outline`, `impact`, `arch`, `query`, `validate`, `source-search` | recorded |
| `status --json` | **live** |

A caller that learned `head == current` from `find` and applied it to `status`
read a stale check as fresh. The documented key set was worse than wrong: it
listed `head` and `current` together, which only makes sense as recorded-vs-live,
and `cmdStatus` emitted neither half of the pair.

The damage was confined to machines. A human reads the rendered row
(`clean`/`dirty`, `z:ok`/`z:stale`) and never saw the JSON. A machine got
`state: "dirty"` with no inputs to verify it against — a verdict it could not
audit.

## The decision

**`cmdStatus` calls `freshness()`, so `head` means the recorded commit
everywhere.**

`03-COMMANDS.md` documented four keys for `status --json`; the row now carries
all five that `freshness()` produces — `head`, `current`, `fresh`,
`zoekt_head`, `zoekt_fresh` — plus the ones only status has: `state`, `path`,
`mode`, `fingerprint`.

This flips `head` on `status --json` from live to recorded. That is a change to
a shipped key, accepted deliberately: tk is unreleased, `--json` envelopes
carry no compatibility guarantee, and a caller who needs a shape to hold still
pins a commit. The alternative was keeping `head` live and adding `current` as a
duplicate, which buys back-compat at the price of the collision above — and
compatibility with an unreleased shape is not a real cost to pay.

`state` stays. `freshness()` has no equivalent, and it is the rendered verdict a
human already reads, so the two faces continue to agree: `fresh:false` implies
`state:"dirty"`, and both tests pin it.

## One resolution, not two

`freshness()` resolved the live side itself, so calling it from `cmdStatus`
meant resolving the tree twice per project — `git rev-parse` twice for a git
project, and for a plain directory a full `filepath.Walk` **twice**, since
`store.Fingerprint` is a stat walk of the whole tree.

So the live side became a value:

```go
type liveState struct {
    head string // commit, for a git project
    fp   string // fingerprint, for a plain directory
}
```

`freshnessFrom(proj, live)` takes it; `freshness(proj)` resolves it and
delegates. The two forms are kept apart rather than flattened into one string
because which yardstick applies is not something a caller should re-derive by
guessing whether a value looks like a commit or a fingerprint.

`cmdStatus` resolves it once and feeds both the state verdict and the envelope
row, so the extra walk is gone and there is still exactly one definition of what
`fresh` means.

## The tests

`internal/cli/cli_test.go` runs `cmdStatus` and reads the envelope, rather than
calling `freshness()` directly — the bug was in which function the command
reached for, so testing the function would have passed before and after.

* clean tree — all five keys present, `head == current == <commit>`, `fresh:true`
* registry behind HEAD — `head != current`, `fresh:false`, `current` is the live
  commit: the pair a machine needs to compute staleness
* plain directory — `head` and `current` both carry the fingerprint, and
  `zoekt_fresh` requires `files`
