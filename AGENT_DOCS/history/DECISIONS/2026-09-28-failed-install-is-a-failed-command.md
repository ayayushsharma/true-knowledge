---
title: A managed install that did not install is a failed command
status: authoritative
date: 2026-09-28
supersedes: null
superseded-by: null
---

# A managed install that did not install is a failed command

## The gap

`tk install` collected a per-backend row, and on error it printed the row and
moved on:

```go
plan, err := installer.Install(...)
if err != nil {
    rows = append(rows, row{name, "failed", err.Error()})
    lines = append(lines, fmt.Sprintf("%-8s FAILED: %v", name, err))
    continue
}
```

The command then rendered its table through `ctx.out` and returned `nil`. Exit
code 0. The `ok` field in the `--json` envelope was hardcoded `true`.

So `tk install` reported success for an install that did not happen. The
concrete damage was in the real e2e gate, whose install check is the one line
that cannot be softened:

```go
if r := h.run(t, "install", "cbm"); r.code != 0 {
    t.Fatalf("install cbm: %s", r)
}
bin = filepath.Join(h.home, "cache", "bin", "codebase-memory-mcp")
```

The guard could never fire. The run continued, and the failure surfaced three
steps later as "binary does not exist" — a symptom pointing at the resolution
order, the cache layout, and the pin, none of which were involved. A user
reading the same output was told the install was fine, in the same table, on
the same line, as a successful one.

This predated the delegation to the vendor installer. It was latent because tk's
own installer almost never failed. Delegating made install fallible, and a bug
that was theoretical became the reason a consumer-laptop gate could not run.

## Two more lies the same table told

**The reason was discarded.** The vendor script's output went to `os.Stderr`
and the returned error carried only the exit status. CBM's own refusal messages
name the check that refused:

```
error: activation could not reserve exclusive access; no activation was committed.
error: this is NOT a running-session problem — the reservation itself failed
```

That text was replaced by `run install.sh: exit status 1`. A full disk, a
corrupt archive, and a daemon that refused to drain are the same three
characters. Diagnosing a consumer-laptop install meant rebuilding the vendor
tree by hand to read the code that produced a message nobody was shown.

**Exit 0 was accepted as proof the pin landed.** CBM exits 0 in two cases where
nothing was published at `<cache>/bin`: a binary owned by mise, Homebrew or
nix is left alone with "Leaving it and your PATH untouched", and a
config-only install publishes no binary at all. tk reported `installed` and
wrote a pin that no binary backed. The next run then read the version as `-`
and the command had no honest answer.

## The decision

1. **A backend that did not install is a failed command.** `tk install` renders
   its table, then returns a non-zero error naming the backends that failed.
   `tk setup` does not: it stays fail-open, because a missing backend is one
   `tk install` away and must never block an agent.

2. **The two faces agree.** `ok` was hardcoded `true` in `Ctx.out`, so the
   envelope claimed success next to a non-zero exit. `Ctx.envelope` now takes
   the verdict, and `outFailed` renders it. A machine reading `--json` and a
   shell reading `$?` cannot now be told different things.

3. **The backend's diagnostic is carried, not replaced.** The script's combined
   output is kept while it streams to stderr, and the last whole lines of it
   become `Error.Detail`. Whole lines, counted, with the count marked: half a
   tar or activation message is worse than none of it, because a truncated
   message is the one thing that must not look complete.

   The writers need a mutex. `exec` copies stdout and stderr on two
   goroutines; a plain buffer loses whichever write loses the race, which is
   how a tar error disappears from the diagnostic meant to carry it.

4. **The daemon hint is additive, and admits it is brittle.** A daemon still
   holding the coordination lock is the one install failure a reader can always
   clear, and tk names `tk daemon stop` when the output mentions a daemon,
   session, cohort, lifetime or activation. It matches the vendor's prose,
   which CBM can reword. So it only ever adds a line: a rewording costs the
   hint, never the diagnosis in `Detail`.

5. **The pin is verified, not assumed.** After the script exits 0, tk stats
   `<cache>/bin/<binary>` and reads its version. No binary, a binary that will
   not report a version, or a binary at the wrong version is a failure with
   `errors.Is(err, errNoPin)`. This is the one thing tk owns, so it is the one
   thing tk checks.

6. **`TMPDIR` points inside the cache.** `install.sh` unpacks a 40 MB archive
   into a 300 MB binary inside `mktemp -d`, which defaults to the system temp
   dir; on a small tmpfs that is ENOSPC. The work dir is private, 0700, beside
   the target, and removed after.

   It does not move everything. The backend stages its own prepared candidate
   under `cbm_tmpdir()`, which its source documents as deliberately separate
   from `$TMPDIR` on POSIX. tk does not reach past the vendor's contract to
   relocate that copy. What tk does instead is stop being silent when that
   space runs out — which is why (3) is the load-bearing change and (6) is
   only mitigation.

## What this costs

The human face of `tk install` grows: the FAILED line carries the reason, then
indented detail lines, then sometimes a hint. That is a real change to a
protected surface, bought with the loss of an hour per install diagnosis and a
gate that could not see its own first step fail.

## What stays

`tk setup` fails open. `tk` never stops the shared daemon itself — it prints
the command to run. A CBM daemon from a version that predates the drain
protocol cannot be asked to quiesce, so CBM refuses rather than guessing; tk
surfaces that refusal and the fix rather than reaching around it.
