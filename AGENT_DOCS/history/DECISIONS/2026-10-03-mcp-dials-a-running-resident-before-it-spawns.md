---
title: An MCP graph call dials a running resident before it spawns
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# MCP dials a running resident before it spawns

## The gap

`Ctx.residentTry` (`internal/cli/root.go`) dials the resident socket first and
spawns only when nothing answers, so every `tk` CLI read has had a warm path
since the resident shipped. `internal/mcp` never got it: all ten engine call
sites go straight to `s.Run.RunJSON` / `s.Run.RunStructured`, and `s.Run` is
the spawn.

`AGENT_DOCS/01-ARCHITECTURE.md` states the rule without the restriction —
"reads dial first and fall back to a one-shot spawn" — so the doc described a
property only the CLI had. `tk mcp` is not a lesser citizen: it is the transport
for every hook-less agent client, and it is the *only* transport for pi, codex,
cursor and opencode.

Measured on CBM 0.11.0, this repo, `moderate`, resident running and serving CLI
reads in 12–26ms:

| path | `search_graph` hit | empty `search_graph` | three graph calls |
|---|---|---|---|
| cold spawn | 4911ms | 9405ms | 10370ms |
| warm spawn | 2008ms | — | 5356ms |
| resident socket | ~15ms | ~26ms | ~50ms |

The warm-spawn column is the one that matters: with a resident up and healthy,
`tk mcp` still paid 2.0s per call because nothing dialled. 130× for the dial,
and it is the difference between a 5.4s turn and a 0.1s turn.

## The decision

`internal/mcp` asks a resident first and spawns second, with the three outcomes
kept distinct exactly as `Ctx.residentTry` keeps them:

* **absent** — `Call` returns any error (no socket, dead resident, EOF with no
  reply): fall through to the spawn. The resident is opt-in, so this is the
  normal path and is not a fault.
* **refused** — `Reply.Error` is set: propagate it. The engine said no; a fresh
  process would say the same thing, slower. Folding a refusal into "absent"
  reports a failed call as a successful empty result, which is the one outcome
  the coverage rules exist to prevent.
* **answered** — parse `Reply.Result` with the same `cbmexec.ParseResult` the
  CLI uses, so one engine answer cannot render two ways depending on which
  process asked.

Two implementation choices worth naming:

* **The dial lives in `Server`, not in a decorator around `s.Run`.** Ten call
  sites become `s.runJSON` / `s.runStructured`, and `s.Run` keeps its meaning:
  the spawn. The request loop is strictly serial (`s.handle` runs inline), so a
  single field records which path served the call and `tk.log` gains a
  `backend` key without any locking.
* **`s.Resident` is an interface, not `*resident.Client`.** The production value
  is the real client; the test value is a stub, and one test additionally
  serves a real unix socket with `resident.Encode`/`Decode` so the wire format
  is exercised rather than assumed.

## What this does not do

* **It never starts a resident.** `tk mcp` dials a socket that may or may not
  exist. Autostart from a client would put a long-lived process behind a
  model's tool call, and it needs its own ADR and config key; the documented
  behaviour stays `tk mcp --detach`.
* **It does not touch writes.** MCP has no engine write tools; the memory
  stores are in-process and untouched.
* **It does not change the resident's contract.** One request per connection, no
  multiplexing. The measured 10–16ms per round trip is what makes a 5-call
  compound ~75ms, which is why the budget work adds compounds rather than
  concurrency.
* **`EnsureIndex` and the zoekt freshness path still run in this process.**
  They are git work, not engine work, and they must see the live tree.

## Consequence for the latency budget

The SLO in `AGENT_DOCS/THROWAWAY/2026-10-03-latency-budget-and-operator-surface.md`
is written against the dial path. Without this change every green row is 2008ms,
which is 20× over the hard ceiling — so this is the change that makes an
in-loop sub-100ms call possible at all, not an optimization on top of one.
