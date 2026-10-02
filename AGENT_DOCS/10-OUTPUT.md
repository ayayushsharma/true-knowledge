---
id: 10-output
title: Output channels — stdout is the answer, stderr is the story
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-10-02-stderr-is-the-diagnostic-channel.md]
superseded-by: null
---

# 10-OUTPUT — the two channels

## The rule

**stdout carries the answer and nothing else.** Every byte a caller might want to
pipe is the only byte on stdout. `tk install --json | jq .ok` yields a valid
envelope; `tk status --json > s.json` yields a file; `tk note toc > toc.md` yields
markdown. No flag changes this — not `--json`, not `--verbose`, not `--quiet`.

**stderr carries everything else:** progress steps, warnings, debug narration, and
the engine's own refusals.

This is structural, not conventional. `internal/logx` and `internal/progress` hold
no reference to `os.Stdout`, and `TestNeverWritesStdout` proves it by capturing
the real descriptor and asserting nothing arrives.

## Live channel and record are not the same sink

| Sink | Job | Lifetime |
|---|---|---|
| stderr (`internal/logx`, `internal/progress`) | the live view: is it still working, what did it decide | gone when the command ends |
| `<state>/logs/tk.log` (`internal/trace`) | the record: argv, timings, engine payloads, events | 10 MiB ×2 rotation |

Neither does the other's job. Duplicating the record onto stderr would put engine
payloads in front of a human waiting for a download. Not duplicating the narration
into `tk.log` would leave "why did that take 40s" unanswerable. So `logx.SetRecord`
tees each stderr line into `output.diag` in the trace record, and the two agree:

```console
$ jq -r '.output.diag' tk.log | head
replace codebase-memory-mcp 0.11.0 — currently - at (missing)
[3/8] download codebase-memory-mcp — 340.2 MiB in 41.7s (8.2 MiB/s)
```

There is still no `tk log` command. The file is jq-native by design.

## Levels

| Knob | Effect |
|---|---|
| `TK_LOG=off\|error\|warn\|info\|debug` | level; default `warn`. An unrecognised value warns and falls back rather than silently producing no logs |
| `--verbose` | level becomes `debug` |
| `--quiet` | level capped at `error`, and the step reporter is silenced |

A flag beats `TK_LOG`: a flag on the command line is the more specific statement
of intent. The default is `warn` and not `off` because a degraded run that says
nothing until the final table is exactly the failure this exists to prevent.

Every line passes the same high-precision secret redaction as `tk.log`. stderr
gets pasted into issue trackers; a guard that covers one channel guards nothing.

`tk mcp` forces progress off: its stderr is captured by the agent client as part
of the session record, and the engine is addressed over stdio, so an in-place line
there is a foreign object in someone else's transcript. Debug logging stays
available behind `--verbose` for the operator debugging a slow tool.

## Progress semantics

* **Steps are unconditional; live frames are terminal-only.** A completed step is
  the unit of output and lands on every destination. An in-place byte counter or
  bar is drawn only on a TTY and writes nothing elsewhere — an unfinished thing is
  not a report, and a half-drawn bar in a log is a lie about how far the work got.
* **Three kinds of line, and the distinction is load-bearing.** `Step` is one unit
  of completed work and consumes a number. `Note` is detail under the line above:
  indented, unnumbered, so an environment-dependent line cannot push a fixed
  denominator. `Phase` is a heading: unindented, unnumbered, because nothing
  finished under it yet. `tk install` opens with one phase per backend and runs a
  fixed eight-step pipeline under it.
* **A declared total is never exceeded.** `Total(n)` declares a fixed run and
  resets the counter, so a command can have a heading, some environment-dependent
  notes, and then a numbered pipeline without moving a denominator. A position is
  omitted entirely when the total is unknown; a guessed denominator is worse than
  none, and `[10/9]` is worse than both.
* **Every stage that can fail reports, including on the failure path.**
* Best-effort throughout. No write here returns an error, and none can fail a
  command that would otherwise have succeeded.

## Watching an install

On a terminal the download draws an in-place byte counter. Redirected, the
completed steps land as plain lines, so `tk install 2>install.log` is a readable
transcript rather than thousands of carriage returns.

```console
$ tk install cbm
replace codebase-memory-mcp 0.11.0 — currently - at (missing)
       nothing holding the old image — no quiesce needed
[1/8] resolve codebase-memory-mcp 0.11.0
       archive https://…/codebase-memory-mcp-linux-amd64.tar.gz
       target  ~/.cache/true-knowledge/bin/codebase-memory-mcp
[2/8] fetch checksum manifest — 105 B in <10ms
       want sha256 032b33c1f7e2…
[3/8] download codebase-memory-mcp — 340.2 MiB in 41.7s (8.2 MiB/s)
[4/8] verify sha256 032b33c1f7e2… — 1.4s
[5/8] extract codebase-memory-mcp — 302.1 MiB in 2.1s
[6/8] validate staged candidate — reports 0.11.0 (<10ms)
[7/8] publish ~/.cache/true-knowledge/bin/codebase-memory-mcp — <10ms
[8/8] validate published binary — reports 0.11.0 (<10ms)
cbm      installed 0.11.0 -> /home/you/.cache/true-knowledge/bin/codebase-memory-mcp
```

The quiesce and resume lines are unnumbered notes under the heading because they
depend on what happened to be running; the numbered pipeline is fixed. A first
install says `nothing holding the old image`, because silence there is
indistinguishable from a skipped stage. `tk index` and `tk sync` have no heading
at all — two steps per project, project name inside each — so the count stays
exact. `tk setup` runs the identical pipeline.

## Spawn narration

`--verbose` adds each engine spawn from inside `internal/cbmexec.spawn`: argv,
wall time, exit verdict, and both stream sizes.

```console
[tk] debug: cbmexec: spawn /…/codebase-memory-mcp cli --json search_graph --args-file /tmp/tk-args-1
[tk] debug: cbmexec: cli --json search_graph: ok in 4.9s | stdout 18.2 KiB | stderr 0 B
```

This is the one that matters for latency. The two format fallbacks re-run a tool
behind the caller's back, so a reader with only the caller's trace event sees one
spawn where two happened, and a 9-second call has no explanation anywhere.
Narrating inside the wrapper means the second engine process shows up without a
code change at each call site.

## What a resident records

`tk mcp --detach` outlives every command that talks to it, so
`<state>/logs/resident.log` is the only place a served call can be recorded after
the fact: one line per call with the tool, the elapsed time, and whether the
engine answered. It is a separate file from `tk.log` because it outlives the
command that spawned it.