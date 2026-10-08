# `find` and `explain` over MCP

Status: **plan, not built.** Phase 2 item 1. Written 2026-10-03.

## The issue, in one line

The CLI has two compounds built for a slow model and the MCP surface — the only
one Codex, Cursor, and OpenCode get — has neither, so those clients pay three
round trips and three chances to mis-call for one intent.

## What exists, measured

| | CLI | MCP |
|---|---|---|
| router | `tk find` (`internal/cli/query.go:207-215`) | **absent** |
| compound | `tk explain` (definition + traversal) | **absent** |
| a 30B client's route to the same answer | `tk explain --symbol X` | `search_graph` → `get_code_snippet` → `trace_path` |

Engine cost is already known, measured over the resident socket (median of
seven, CBM 0.11.0): `find` natural **30.4ms**, `find` empty **28.9ms** (2 round
trips — the second is the coverage gate), `explain` **28.3ms** (2). So the
compounds cost about what one of their three constituent calls costs.

**The justification is not latency.** Three separate calls already cost ~45ms of
engine time. What `find`/`explain` buy is one tool call instead of three, fewer
output tokens, and fewer mis-calls. A 30B argument, and the one the ADR must
make — borrowing the P1 latency number here would be the wrong reason.

## What changes in the contract

`04-MCP.md` says multi-call **commands** compose their own `data`, and that CBM's
payload passes through verbatim. The first half is about the CLI and stays true.
The second half stops being true for these two tools: their payload is tk's, not
the engine's. So:

1. New ADR: "two MCP tools are composed, the rest pass through", with the
   composing list named explicitly.
2. `04-MCP.md` states which tools are pass-through and which are composed, so a
   client is not left inferring it from a payload that happens to nest.

## Decisions to settle before writing code

* **Shape.** Match the CLI's `--json` composition, or flatten to the engine's
  payload plus a `route` field? `find` already reports `route` today
  (`fields: map[string]any{"route": tool}`). Parity with the CLI argues for the
  composed shape; a smaller schema argues for flattening.
* **Schema cost.** Both tools are new schemas on every turn. Measure
  `tools/list` bytes before and after and record it; if the pair costs more than
  it saves, that is the finding, and `compact` (item 2) is where it belongs.
* **Empty-result coverage.** AGENTS.md rule 4: a hit costs 1 call, a miss 2, and
  the verdict rides on the result. The compounds must carry the same annotation
  the constituents carry, or the MCP face loses the only thing that makes a
  negative claim defensible.
* **Argument errors.** `--label` with a regex-shaped query fails in the CLI with
  a named message. Reproduce that as `isError` text, not as `-32602`: the model
  can read and correct prose, and the CLI's wording is already the wording a 30B
  has seen.
* **`limit`.** The CLI caps at 20 by default. The schema needs the same default
  stated, because a schema that says "optional limit" invites an unbounded call
  whose output is the expensive part.

## Build order and gates

1. `find` first, no compound — port the three-way router, measure the schema,
   assert row-for-row parity with `tk find` on the same project.
2. `explain` second — it is the case with the strongest argument (it is the one
   the 27B operating rules in `04-MCP.md:184` already tell a model to prefer).
3. Then the ADR, then `04-MCP.md`. Code first here because the ADR's claim is
   about a shape that exists; an ADR written before the shape exists is a guess.

Gates, each falsifiable:

* `tools/list` growth recorded, with the token figure.
* Same project, same query: MCP `find` and CLI `tk find` return the same rows
  and the same coverage verdict.
* An empty result still carries the absence annotation, on both faces.
* An e2e assertion beside the existing `resident-render-parity`, so the compound
  is checked through the resident and not only through a spawn.