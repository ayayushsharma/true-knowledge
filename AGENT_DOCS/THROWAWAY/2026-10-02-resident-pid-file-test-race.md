# The resident pid-file test races on `listen()`

Found 2026-10-02 while running `-race` after adding stderr progress. Not caused
by that change: it reproduces with `internal/resident/server.go` stashed.

## What happens

`TestResidentWritesAndCleansPidFile` fails roughly one run in eight under
`-race -count=8`, with:

```
server_test.go:304: the pid file does not name a live process while the resident is up
```

`Server.Serve` orders its startup as:

1. `s.listen()` — binds *and* listens on the unix socket
2. `writePid(s.PidFile, os.Getpid())`
3. `s.ensureEngine()`
4. the accept loop

`waitFor(t, addr)` in the test returns as soon as the socket accepts a dial.
Between (1) and (2) there is a real window where a client can dial a socket that
has no pid file behind it yet, so the assertion runs first and loses.

`-race` widens the window; without it the test passes. Under a loaded parallel
run it will flake too.

## Why it is not fixed here

The obvious fix is to reorder — write the pid file and attach the engine
*before* `listen()`, so a dialable socket is a promise the resident is already
able to keep. That is arguably more correct than today's order (today a client
can connect to a socket whose engine has not started, and then sit in the accept
queue until the read deadline).

It is not this change's business, because the resident's startup ordering is
governed by
`history/DECISIONS/2026-09-30-resident-owns-its-endpoint-and-its-swap.md` and by
the read-deadline reasoning in the `serveConn` comment. Reordering startup
without its own ADR would violate rule 8 in `AGENT_DOCS/00-INDEX.md` — and this
is a test-timing defect, not a user-visible one.

## What to do

Verify the window is still there on a current build, then either move `writePid`
and `ensureEngine` above `listen()`, or change the test to wait for the pid file
rather than for the socket. The first is the better fix and wants an ADR; the
second only hides the window and leaves a client able to dial a half-started
resident.

## The transferable part

A readiness signal that a test waits on is only a readiness signal if it is the
last thing before serving. `listen()` returns before the process is ready to
answer, so it is a *liveness* signal, not a readiness one. Same shape as the
spawn-count and "no mechanism exists" traps in `INDEX.md`: a green signal
observed at the wrong moment reads as evidence for a claim it does not support.