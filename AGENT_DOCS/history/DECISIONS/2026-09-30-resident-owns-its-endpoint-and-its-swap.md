---
id: 2026-09-30-resident-owns-its-endpoint-and-its-swap
title: The resident owns one local endpoint, and the install swaps the engine on it
status: authoritative
date: 2026-09-30
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md]
superseded-by: null
---

# The resident owns one local endpoint, and the install swaps the engine on it

History is immutable, so this supersedes
`2026-09-30-resident-warm-child-needs-no-freshness.md` to do the two things it
explicitly deferred. It said a detached resident puts `AGENTS.md`'s
no-supervisor-daemon rule and `02-BOUNDARY.md`'s no-PIDs-or-endpoints rule "in
play", and that they "will be withdrawn by the ADR that lands the resident, not
by this one, because the resident is not built". The resident is built. This is
that ADR.

The previous ADR's other decisions are unchanged and are not restated here: one
warm child, no freshness machinery, explicit start only, no idle timeout, writes
stay one-shot, results forwarded verbatim. Read it for those. What follows is
only what landing it forced.

## Withdrawn

`02-BOUNDARY.md` said that tk managing "PIDs or endpoints" is a bug. That rule
exists to stop tk reimplementing CBM's coordination, and tk still does not: it
never opens a database, never parses source, and never signals CBM's daemon. The
resident does not reach into the account-wide namespace at all. It owns exactly
one socket and one pid file, both under its own `TK_HOME`, both naming its own
process.

That is the test, and it is narrower than "tk manages an endpoint": **does the
thing tk manages name a process tk started, in a directory tk chose?** For the
resident, yes on both counts. For a CBM daemon, no — so `tk daemon` stays
CLI-only and the account-wide daemon stays shared and never tk-owned.

`AGENTS.md` keeps its own line, "there is no `tk` supervisor daemon", and keeps
it without amendment. The resident is a child of `tk mcp --detach` holding one
CBM process; a supervisor is a process that starts, watches, and restarts
daemons for an account. The resident watches nothing and restarts nothing, and it
dies on SIGTERM rather than being respawned. The distinction is that the earlier
latency ADR got wrong by reading the resident as a daemon, and it is the reason
that ADR was superseded.

## The control channel is the same socket

`tk install cbm` must replace the CBM binary under a resident that the user is
talking to, and must not kill it. The resident's engine is therefore replaced in
place, through three control actions carried on the tool socket: `status`,
`quiesce`, `resume`. It is a closed set, not free-form verbs.

The alternatives were a second socket, a signal, or a file. A second socket
doubles the endpoint surface this ADR just justified halving. A signal has no
reply, so it cannot report "the swap failed" or "I am quiesced now", and every
such question becomes a poll. A file is a lock in disguise and would have to be
polled for the same reason. One socket, one verb set, one reply each.

The rule that makes it safe: **control actions are authenticated by the socket's
own permissions, exactly as tool calls are.** Nothing new is exposed, so the
resident's existing owner-only mode is the entire access story.

Two orderings are load-bearing, and both were bugs before they were rules.

**`quiesce` takes the serial slot before closing the engine.** It sets the flag,
then acquires and immediately releases `one`. Holding that mutex is the proof
that no request is in flight, so a slow query is not cut off mid-answer. A
request that arrives after the flag is set is refused with `ErrQuiesced` rather
than queued — the client has a one-shot path, and a read queued behind an
install is slower than the spawn it was avoiding.

**`resume` clears the flag only after the engine is attached.** Reversing this
opens a window where the resident claims to serve but has no engine, so a client
arriving mid-swap is told "no engine" and reads a broken resident rather than an
upgrade in progress. A failed start leaves the resident quiesced on purpose: a
resident with no engine is not a serving resident, and the honest answer is to
keep refusing so callers fall back to a spawn.

## Readiness needs a pid, not a dial

The first implementation waited for the socket to accept, then declared the
resident up. That is a dial succeeding while the engine is still being spawned —
so `tk install` could return before the resident could answer, and the first
real request would take a spawn anyway.

`status` therefore reports the resident's pid **and** its attached engine pid,
and readiness requires the engine pid to be non-zero. A socket that accepts but
has no engine is a resident mid-swap, and reporting it as warm is the same lie
in a new place. `EnginePID` is 0 while quiesced, which is the one case where
"accepts" and "can answer" differ, and it is exactly the case a client needs to
distinguish from a dead socket.

## What the transport is not

The resident forwards a tool name and arguments and returns CBM's `result`
object verbatim. It does not parse, wrap, reformat, or make policy decisions, so
`cbmexec.parseEnvelope` produces a byte-identical `cbmexec.Result` on both
faces. It is not a reuse of `internal/mcp.Server`, which forces
`format:"json"` on structured tools and would collapse `tk find`'s human table
into structured output.

Each connection carries one request and is closed. Connections are served on
their own goroutines, and the engine is still driven one call at a time by the
`one` mutex — the serialization that matters is the engine's, and it holds for
any number of goroutines.

## Two things that were true only on the machine that wrote them

Both were found by asking what the code does rather than what the comment says,
and both are the same mistake in different clothes: a claim that the test suite
could not fail.

**Connections were handled inline in the accept loop.** Measured: a warm read
took 55ms; with one client connected and saying nothing, the next unrelated read
took 19549ms and then returned the *correct* answer, because tk exhausted its
fallback budget, spawned a one-shot engine, and served that instead. The
resident was not crashed or wedged — it was made irrelevant by a peer that
opened a socket and said nothing, with no error anywhere.

tk creates exactly those connections itself: `CallTimeout` abandons a slow
request without closing the connection, because the child must stay alive. So
"clients are well behaved" was never a property of this server. The test that
claimed to cover this had closed its silent peer before the assertion that
mattered.

**`PidAlive` reported every pid alive off-Unix.** Its "cannot ask, so it does
not claim" comment described a no-op `os.Signal` whose `Signal()` returned nil.
A stale pid file would have read as a live resident forever, and the comment
said the opposite of the code. A reporting helper that cannot ask must answer
"unknown", and the only honest encoding of unknown in a bool is false: the caller
reports no resident, a gap someone can act on, rather than a warm resident that
does not exist. It is not used for takeover, so the conservative direction costs
nothing.

Also corrected here, because both were platform-shaped assumptions that would
have failed on arrival: the spawn-accounting test matched `/tmp/tk-args-` while
`os.CreateTemp` uses `TMPDIR`, so on macOS every subtest failed; and a socket
path past `sun_path` failed with `bind: invalid argument`, naming neither the
cause nor the fix when the cause is a `TK_HOME` the user chose. The limit is
prechecked at 103 bytes — macOS's, not Linux's 107 — because a tk that starts on
one machine and moves to the other must not fail on arrival.

## The generalisable lesson

The measurable version of rule 10 in `AGENTS.md`: **a test that cannot fail is
worse than no test, because it reports a guarantee nobody checked.** Four of
these were found in one sitting, and two of them — the vacuous timeout test and
the silent-connection test — had comments describing coverage they did not
provide. The timeout test aimed a nanosecond deadline at an instant fake and
skipped itself when the fake won, which it always did.

When a comment claims a property, the cheapest check is to make the property
fail on purpose: add `c.Close()` to the timeout path and watch the test go red.
A test that survives that experiment is testing the implementation's shape, not
its guarantee. Each of the four here failed within a minute of being asked.
