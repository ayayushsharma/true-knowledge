# THROWAWAY — scan this before you search

Working notes. **Never authoritative.** A note here that contradicts
`AGENT_DOCS/NN-*.md` is wrong and gets deleted, not obeyed.

## Why this exists

Every question an agent is asked has a half-answer somewhere. Some of it was
discovered by measurement, some of it is a dead end already walked once. Both
are expensive to rediscover and cheap to record. This directory is that record.

## The rule

**Read `INDEX.md` here before any long search** — before grepping the repo for
background, before reading upstream docs, before web search. Not instead of
verifying: *instead of rediscovering*. Check the index, then verify anything
that matters. `AGENTS.md` makes this mandatory.

## How to write one

Filename `YYYY-MM-DD-topic.md`. No front matter. Prose over ceremony.

Every note answers three questions in its first lines:

* **Question** — what someone would plausibly ask that this answers.
* **Status** — `measured` (has real numbers), `read` (someone else's claim),
  `inferred` (reasoned, unverified), `dead-end` (tried, did not work).
* **So what** — what to do, or why to stop.

## Lifecycle

| Event | Action |
|---|---|
| Discovery | Write the note. Cheap. Do it while you have the context. |
| Becomes a decision | Promote: new dated ADR, then the rule in the affected `NN-*.md`. Note gets deleted here, not left to rot. |
| Contradicted | Delete, and delete loudly. A stale note costs more than no note. |
| Folded into an authoritative doc | Delete. The authoritative doc is the record. |

Notes are mutable. That is the whole difference from `history/`, which is
immutable and enforced by `go test ./internal/docs`.

## What does not go here

Anything already answered by an authoritative doc, anything with no "so what",
and anything that is a decision rather than an observation. Those are ADRs.
This directory is for the messy middle: findings, dead ends, half-verified
hypotheses, and the questions we have not answered yet.
