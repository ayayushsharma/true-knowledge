# Resident session lifecycle: tk upgrades, CBM swaps, and backpressure

**Status: mixed.** Corrected 2026-09-30. §§1-2 (upgrades, binary swaps) are
`inferred` design and still stand. **§3, §Lifecycle, and §Test isolation were
wrong and are withdrawn** — see `2026-09-30-warm-child-store-freshness.md` and
`2026-09-29-cbm-request-concurrency.md`. Nothing here is built. The decided
shape is `AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md`.

## The goal, stated as an invariant

> **The same tool through the human CLI and through MCP must cost the same.**

Today it cannot: the human CLI pays 4.9s per call and the MCP path pays 74ms,
purely because the CLI's process is too short-lived to keep the engine warm. The
target is not "make the CLI faster than agents" but "stop making it slower than
agents". One warm child, two ways in.

This is checkable, so it should be an e2e assertion rather than a hope: run
`tk find` and the equivalent MCP `tools/call`, assert medians within a small
tolerance. If that test cannot be written, the design has failed.

## Shape

A resident is **`tk mcp --detach`** holding one CBM MCP child behind a local
socket. Two constraints from the rulebook that survive every revision:

* Rule 2 — every CBM spawn goes through `internal/cbmexec` with the env map.
  The child is spawned there, not by the listener directly.
* Rule 5 — fail open. The one-shot path stays; the resident is an accelerator,
  never a dependency. An unreachable socket means the command spawns as today.

Reads only. Mutations (`index_repository`, `manage_adr`, and anything holding a
per-project lock) keep the one-shot path. A long-lived process holding the
admission lease through an index is a worse trade than a 5s write.

**The resident is a transport, not a query layer.** It accepts a tool name and
arguments, forwards to the child, and returns CBM's `result` object verbatim.

**It is not `internal/mcp.Server`.** That server is a policy layer: it resolves
profiles, annotates absence, and — the part that matters — calls
`RunStructured` with `format:"json"` for structured tools. Reusing it in the
resident would force structured JSON where `tk find` today prints a human
table, breaking the two-faces-equal invariant from the other direction. The
six render-parity tests in `internal/cli/cli_test.go` are the guard. This is
why the earlier claim in this note — "all dispatch, profiles, absence
annotation, and `Data` handling already exist, nothing else is new logic" — was
wrong: reuse here is a *behaviour* change, not an implementation detail.

## 1. tk CLI updates

A resident is started by a specific tk build. A new tk must never speak to an
old resident: request/response shapes drift, and the resident may hold a
different CBM binary than the new tk resolves.

**Identity, not version-string sniffing.** The session writes a small
`session.json` beside its socket containing everything that must match:

```
tk protocol version, tk build, CBM binary path, CBM version,
CBM_CACHE_DIR, CBM_RUNTIME_DIR
```

A client reuses the session only if every field matches its own resolved
values. Any mismatch means **stop it and start fresh** — do not negotiate, do
not upgrade in place. Negotiation between an old server and a new client is how
you get silent shape mismatches.

The protocol version is a tk-owned constant, bumped by hand whenever the wire
shape changes. It is the one field that cannot be derived from the others.

**This section's mechanism is now largely redundant and should be re-tested
before it is built.** The child is already swapped on every `tk install cbm`, so
a resident is a transport in front of an engine it does not version. If the
resident's own wire shape is just "tool name in, `result` out", the fields above
reduce to the CBM path, the cache dir, and the runtime dir. A stale-CBM resident
is already the case §2 handles. Do not build a six-field handshake until a
concrete drift scenario is demonstrated.

## 2. CBM dependency updates — stands

Upstream `#session-coordination-daemon` on activation:

> Activation then publishes account-wide maintenance intent, asks the daemon
> and every temporary local operation to cancel, and waits to a finite
> deadline for all coordinated CBM processes to exit. It holds the admission
> and lifetime barriers exclusively while changing the active binary.

**A resident's child is one of those processes.** While it holds the admission
lease, CBM's own activation cannot swap the binary — so a resident makes
`tk install cbm` stop-the-world. Acceptable (installs are rare), and the shape
already exists in tk; a resident is one more participant, not a new pattern.

`internal/cli/admin.go` already does exactly this for the daemon:
`quiesceDaemon` reads `daemon status`, calls `daemon stop`, and returns the pid
it retired; the binary is then swapped; `resumeDaemon` starts a fresh daemon on
the new one, and **only if one was running before**, so a first install never
creates a permanent daemon nobody asked for. `Quiesce.Note()` exists because
stopping a process the user did not name has to be attributable.

Add the resident to that same sequence:

1. `quiesceResident` — tell the resident to stop accepting, drain the in-flight
   request, terminate its CBM child, and **wait for the child's pid to be gone**.
   Quiesce the child before the daemon: it is the direct barrier holder.
2. `quiesceDaemon` as today.
3. Swap the binary.
4. `resumeDaemon` as today.
5. The resident is still alive. Its next request spawns a fresh child from the
   new binary.

Two rules, both of which the existing code already respects and which a
resident must inherit:

* **Scope is tk's own processes, never the account's.** `quiesceDaemon` runs
  against `<state>/rendezvous` as `CBM_RUNTIME_DIR`, so it "can never stop an
  account-wide daemon a consumer laptop is running". A resident that is
  account-wide, or that spawns a child without that env, breaks the guarantee
  and can kill the user's editor indexing.
* **Wait for exit, do not signal and continue.** The hard part of the swap is
  confirming the process is gone, not sending it a stop. The existing code
  already does this dance; extend it rather than write a second one.

Failure mode to avoid: activation times out waiting for the child and reports
success anyway. A swap that did not happen is worse than one that refused — if
the child will not exit, the install must fail loudly.

**Decided 2026-09-29: swap the child underneath, never kill the resident.** The
only actor who can trigger an update is the user, so this is a simplicity call
rather than a safety one — and swapping wins outright. The resident *is* the
agent's `tk mcp` server; killing it drops every agent session's MCP server,
when swapping the child underneath costs one cold child start on the next query
and changes nothing else. `tk install cbm` replaces a subprocess, not the
process the user is talking to.

The scope rule above is unrelated to who triggers the update and still holds:
the user may have account-wide CBM running in their editor, and `tk install cbm`
must not kill it. Same reason the resident must be stoppable by command and by
signal.

## 3. Backpressure — **withdrawn, was wrong**

~~The child is concurrent, so the client must demux by id, cap in-flight
requests, poison-and-replace on timeout, and tax long queries.~~

**All of that is unnecessary.** The concurrency is real in CBM and the
measurement stands (8-13x text, ~2.8x graph, 12 cores). But the measurement was
dispatched by a hand-written probe that sent N requests before awaiting any.
`internal/mcp.Server` serves one message at a time, so a resident built on it
has exactly **one** request outstanding at all times.

What that removes: id demultiplexing, an in-flight cap, a per-request queue,
poison-and-replace on timeout, and a slow-query taxonomy. A single request has
one reply and a connection close, so a late reply cannot be mistaken for a fresh
answer.

What survives: the contention cost is real if concurrency is ever introduced.
Graph queries contend internally, so N concurrent graph queries are not N times
faster. If a future change pipelines requests, the 2.8x ceiling is the thing to
remember, and the in-flight cap is the seam. Re-measure at that point, not now.

## 4. Freshness — **withdrawn before it was ever built**

This section did not exist. It should have, and it would have been a poller, a
goroutine, an inode comparison, a generation counter, and a crash window, all
built to defend against a staleness that **does not occur**.

Measured: a busy warm CBM child sees an externally republished store within
~2s, with `auto_watch` and `auto_index` both off, because `resolve_store`
(`mcp.c:2276`) re-resolves per call. The premise came from `README.md:697`,
which is scoped to `manage_adr` query modes during a *same-process* reindex.
Full method and the reasoning error:
`2026-09-30-warm-child-store-freshness.md`.

**The resident has no freshness code. If a future change adds any, that change
needs a measurement that contradicts the one above.**

## Lifecycle mechanics — corrected

* ~~**Lazy start.** First read that wants the fast path starts a resident.~~
  **No.** Reads never spawn anything. `tk mcp --detach` is the only way a
  resident starts, per rule 7 and rule 3.
* ~~**Finite idle timeout.**~~ **No timeout.** The resident runs until reboot or
  a signal. A wedged resident is stopped by `tk`, or by killing its pid, not by
  a timer — and an idle timeout would have been the thing making every install
  racy.
* **Stale-socket takeover.** pid file plus a connect probe. A dead process's
  socket file must not be treated as a live session, and must not block a new
  one. Take over, do not fail.
* ~~**`tk session status|stop`.**~~ **No `tk session` verb.** The decision is a
  socket plus a pid file and nothing else; `ps` and `kill` are the Unix answer,
  and rule 8's Unix rule says tk does not ship a command for it.
* **Owner-only socket** under `<state>/`, mode 0700. A local socket that runs
  graph queries is the same trust boundary as the user, but only if it is
  genuinely owner-only.
* **Linux and macOS first; Windows is a named follow-up.** The platform split is
  the existing one in `internal/cbmexec` — `stack_unix.go` (`linux || darwin`)
  beside `stack_other.go` — one sibling file per platform, no build tags at any
  call site. A named-pipe transport and a `CREATE_NO_WINDOW` detach are not
  designed. Recorded so that "Windows first" is never assumed.

## Test isolation — **the concern dissolved**

This section used to call lazy start "the single biggest surprise in the
estimate" and plan `TK_SESSION=off` in every harness. With no lazy start there
is nothing to opt out of: `tk mcp --detach` is explicit, so no test spawns a
resident unless it asks for one. **Do not add a `TK_SESSION` config key** — it
would exist only to disable a behaviour that no longer exists.

The real test cost is different and smaller: `TK_HOME` isolation already gives
each test its own socket path, so parallel tests cannot collide. That comes
free from rule 3.

## What this withdraws

`AGENTS.md` ("There is no `tk` supervisor daemon"), `02-BOUNDARY.md`'s bug rule
("manages PIDs or endpoints ... it is a bug"), and `08-BACKLOG.md`'s "no
supervisor daemon" entry. The ADR that builds it has to say this in the first
paragraph, not bury it.

Note the correction to the record: the previous latency ADR claimed the boundary
rule was untouched because the persistent child "keeps the store open". That
reason is gone — there is no store-lifetime problem, so the exemption rested on
a false premise. A detached resident *does* hold a socket and a long-lived child,
so the rule is in play and must be withdrawn deliberately.

## Open questions

* Does one resident process, holding the lease, measurably slow the *agent's*
  own MCP path or another tk command? Unmeasured. Should be the first benchmark
  after the prototype.
* Memory: one child holds store handles for a 686MB index. What is the
  resident's RSS idle for an hour? Unmeasured, and with no idle timeout it is
  only relevant as a "should I restart this" number for the user.
* Is the 1ms poll a fixed retry budget? If yes, a live daemon cannot fix
  one-shot, and the resident is the only route — which strengthens the case.
* Is the six-field `session.json` handshake (§1) still needed, or does the §2
  child swap cover it? Should be answered by a drift scenario, not by habit.
