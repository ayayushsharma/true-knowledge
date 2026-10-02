---
title: A refused daemon is a failed command, and its committed pids are the diagnosis
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# A refused daemon is a failed command

## The gap

`tk daemon` forwarded to `cbm daemon status|stop` and then decided the outcome
from the shape of stdout:

```go
if strings.TrimSpace(out) != "" {
    return ctx.out(cmd, cbmexec.Truncate(strings.TrimRight(out, "\n"), ctx.budget("")), nil)
}
if err != nil {
    return fail("%v", err)
}
```

CBM exits nonzero for two opposite reasons. It exits 1 to *answer* — `daemon
status` on a stopped daemon prints `daemon: not running` and exits 1. It also
exits 1 to *refuse* — `daemon stop` with committed clients names the pids and
exits 1. A single "did stdout have anything in it" test cannot tell those apart,
and it picked the wrong answer whenever the refusal was written to stdout:

```console
$ tk daemon stop
daemon: refusing to stop; committed clients: 4242 5150
$ echo $?
0
```

`01-ARCHITECTURE.md` already ruled on this for `tk install`: *"A daemon that
refuses to stop — CBM refuses while clients are committed — is a **failed
install**. tk prints the committed pids, fetches nothing, writes nothing, and
exits non-zero. There is no flag that overrides the refusal."* Install honored
it, because install reads `err` and never consults stdout. The verb did not.

The blast radius was small and entirely human: nobody scripts `tk daemon stop`,
and the documented flow for it is a `watcher_enabled` flip. But the two faces
disagreed in the worst way available — `--json` printed `{"ok": true}` beside a
refusal — and the class of bug is the one `2026-09-28-failed-install-is-a-failed-command.md`
was written about.

## The decision

**The exit code is the verdict. Output shape decides nothing.**

A nonzero exit is a failure unless the engine said, in the one shape tk has
pinned, that nothing is running:

```go
if err != nil && !daemonAbsent(out) {
    if text != "" {
        return ctx.outFailed(cmd, text, map[string]any{"sub": sub}, err)
    }
    return fail("%v", err)
}
return ctx.out(cmd, cbmexec.Truncate(text, ctx.budget("")), nil)
```

`daemonAbsent` matches the literal `daemon: not running` and nothing else. It is
deliberately **not** written as `!daemonActive(out)`: an unrecognized shape would
then read as an answer, which is how a reworded refusal becomes a silent success.
Failing closed is the only safe direction for a command whose job is to stop
something.

## Two consequences worth stating

**The pids are not decoration, so they are never truncated.** A budget-folded pid
list is a wrong answer to "which processes must I close". The refusal path uses
`ctx.outFailed`, which renders in full and then returns the error, so both faces
agree — `ok:false` in the envelope, nonzero from the shell — and the text is on
stdout before the error names it on stderr.

**`RunDaemon` was discarding the refusal.** Its own comment promised to
"preserve any daemon output for the caller to interpret", but on the stderr path
it returned `""` and folded the message into an error clipped by `firstLine`.
That made two existing helpers dead code: `busyDaemon` (`admin.go`) has a
`detail` branch for a nonempty `out`, and `indent` exists to lay out a
multi-line daemon report — a shape that could never arrive. A refusal listing
twelve clients lost eleven of them. It now returns the engine's text in full on
whichever stream carried it; `err` is unchanged, so callers wanting only the
verdict still get one.

## Why `daemon start` stays absent

Not an oversight. `01-ARCHITECTURE.md`: *"`daemon start` creates a **permanent**
daemon, so tk only calls it to restore one that was already running. A first
install leaves no daemon behind."* Exposing the verb would hand a human a way to
create an account-wide daemon outside the quiesce/resume discipline that keeps
tk's binary swap safe. `tk cbm` cannot substitute — `RunRaw` prepends `cli`, so
`tk cbm daemon start` reaches `cbm cli daemon start`, a different command.

## Two measured facts about that subcommand

Checked against CBM 0.11.0:

* **`daemon` is missing from the engine's own usage block.** `cbm --help` lists
  `cli`, `install`, `uninstall`, `update`, `config` and the flags — not `daemon`.
  It works regardless: `cbm daemon status` answers `daemon: not running` and
  exits 1, and bare `cbm daemon` prints
  `usage: … daemon <start|stop|status> [--open] [--port=N]`. So tk must not
  decide the subcommand is unavailable from help text, and anyone checking
  whether it is real should run it rather than read `--help`.
* **That not-running answer is precisely the case this ADR exists for.** Real
  `daemon: not running` on stdout with a nonzero exit — which is why the
  predicate matches the prose and the exit code alone cannot carry the verdict.

## The tests that hold it

`internal/cli/daemon_test.go` drives a fake whose refusal stream is a parameter,
because the bug was invisible while the refusal only ever arrived on stderr. The
existing install fake put it there, which is why the defect survived.

* refusal on stdout and on stderr — both exit nonzero, both keep the pids
* unrecognized nonzero shape — fails closed
* `daemon: not running` with exit 1 — exits 0
* `--json` on a refusal — `ok:false`, text intact
* clean stop and live status — exit 0

Reverting `daemon.go` to the old check fails four of them, including the
`ok: true` envelope.
