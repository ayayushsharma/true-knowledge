---
title: stderr is the diagnostic channel, stdout is the answer
status: authoritative
date: 2026-10-02
supersedes: null
superseded-by: null
---

# stderr is the diagnostic channel, stdout is the answer

## The gap

`tk install` downloaded, checksum-verified, extracted, and swapped a ~340 MB
archive behind one silent line. `tk index` shelled into the engine for a minute
and looked hung. Neither printed anything while it worked, because there was no
channel for it — `Ctx.out` was the only renderer in the codebase and it wrote to
stdout.

The rest of the project was quiet too. tk had exactly one diagnostic record,
`tk.log`, and it is written at process exit: it can tell you afterwards that a
call took 9.1s, and it cannot tell you *while you are waiting* that anything is
happening. Nothing was emitted at debug level anywhere, because nothing had a
`Debug` level. So "a lot of logging is missing" was literally true — there was
none to have.

The pressure this created was the wrong kind. A user who sees a frozen terminal
invents a reason to hit Ctrl-C. The available ways out were both wrong:

* Print progress to stdout. Then `tk install --json | jq .ok` receives a bar
  redraw before its envelope and fails to parse.
* Print progress nowhere, which is what shipped.

## The decision

1. **stdout carries the answer and nothing else.** Every byte a caller might
   want to pipe is the only byte on stdout. Progress, warnings, debug narration,
   and the engine's own refusals go to stderr. This is now structural rather
   than conventional: `internal/logx` and `internal/progress` have no reference
   to `os.Stdout` anywhere, and `TestNeverWritesStdout` proves it by capturing
   the real descriptor.

2. **`internal/logx` is the live channel; `tk.log` is the record.** They are not
   the same sink and neither is asked to do the other's job. `tk.log` survives
   the command and carries argv, timings, and engine payloads. stderr is gone
   when the command ends and exists so a person watching knows it is still
   working. Duplicating the record onto stderr would put engine payloads in
   front of a human waiting for a download; not duplicating the narration into
   `tk.log` would leave "why did that take 40s" unanswerable. So `logx.SetRecord`
   tees the whole line into `output.diag` in the trace record, and the two agree.

3. **Level comes from `TK_LOG` and two flags.** `off|error|warn|info|debug`,
   default `warn`. `--verbose` raises it to debug, `--quiet` caps it at error and
   suppresses progress. A flag beats the env because a flag on the command line
   is the more specific statement of intent. Defaulting to `warn` and not `off` is
   deliberate: a degraded install that says nothing until the final table is
   exactly the failure this exists to prevent.

4. **`internal/progress` renders steps, and only steps, unconditionally.** Live
   byte counters and in-place bars are terminal-only; everywhere else they write
   nothing at all. An unfinished thing is not a report, and a half-drawn bar in a
   log is a lie about how far the work got. Completed steps are the unit, which
   makes the transcript identical in a terminal and in `tk install 2>install.log`
   — the only difference is the redraw.

5. **`tk install` reports all eight of its stages, always.** Resolve, fetch
   manifest, download, verify sha256, extract, validate staged, publish, validate
   published. Each carries its own elapsed time, and the download carries bytes
   and a rate. Every stage is a stage that can fail, so the list is the map a
   reader uses to decide where to look. A first install also says that nothing
   held the old image open, because silence there is indistinguishable from a
   skipped stage.

6. **`tk mcp` turns progress off.** Its stderr is captured by the agent client
   as part of the session record, and the engine is addressed over stdio, so an
   in-place line there is a foreign object in someone else's transcript. Debug
   logging stays available behind `--verbose` for exactly the operator debugging
   a slow tool, who otherwise has no live channel at all.

7. **The spawn wrapper narrates its own children.** `cbmexec.spawn` reports argv,
   wall time, exit verdict, and stream sizes. This is the one that mattered most:
   the two format fallbacks re-run a tool behind the caller's back, so a reader
   with only the caller's trace event sees one spawn where two happened, and a
   9-second call has no explanation anywhere. Saying it inside the wrapper means
   the second engine process shows up without a code change at each call site.

## What this costs

The human face of `tk install` grows a second channel. Someone watching the
terminal sees the stages scroll past before the table; someone reading a
redirected stdout sees only the table, which is the whole point. `tk install`
now has a stderr where it had none, and `tk.log` records gained an `output.diag`
key. Both are additive, and under the pre-release law in `00-INDEX.md` neither
is a compatibility promise.

## What this does not change

`tk.log` stays the record and stays jq-native. There is still no `tk log`
command. The boundary rules in `02-BOUNDARY.md` are untouched — narration is
free, and tk still refuses to write anything an operator did not ask for.