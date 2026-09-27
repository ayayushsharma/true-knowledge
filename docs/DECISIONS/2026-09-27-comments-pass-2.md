---
title: Comments pass 2 (config defaults, e2e portability, zoekt walk guards, memory guard depth, MCP cancellation)
status: authoritative
date: 2026-09-27
supersedes: [docs/DECISIONS/2026-09-25-comments-pass-fixes.md (items 2, 3, 4), internal/mcp (package doc tool list), internal/installer (maxArchiveBytes reuse claim)]
superseded-by: null
---

# ADR — comments pass 2 (batch)

Umbrella record for five independent batches. Each was found by re-reading
`comments.md` against the code it described; each is small enough to review on
its own, and together they are one decision: **a comment that states a
guarantee must be enforced at the depth the guarantee is about.** Three of the
five were comments that had quietly become true, and two were comments that had
quietly become false.

`comments.md` is retired at the end of this pass. It predates
`57b948e`, it is a scratch list rather than a design record, and a list of
open items that is itself open is a loop. Decisions live here.

## Batch 1 — config defaults inheritance (`9d87a7c`)

`Config.UnmarshalJSON` unmarshalled straight into the caller's struct, so a
`config.json` that set one key zeroed every key it did not mention. There was
no way to change one setting without rewriting the file with all of them.

`configDoc` is now a pointer-per-field mirror of the on-disk shape, and
`UnmarshalJSON` seeds `Defaults()` before decoding into it, so absent keys
inherit and present groups replace wholesale. A present-but-empty object is
now a reset-to-defaults, which is a shape change worth stating: `{"mcp":{}}`
used to mean "MCP config is zeroed" and now means "MCP config is default".

`GetKey`/`SetKey` moved from `internal/cli/admin.go` into `internal/config`.
They were the second copy of the key registry — the first being the doc struct
— and a key added to one and not the other produced a `config get` that
answered for a key `config set` refused. One registry now, in the package that
owns the shape, with `cbm_version_pin` in the same table as everything else.

Rejected: keeping the CLI copy and syncing by hand (two sources, one bug
class), and a `map[string]any` doc (loses the round-trip test that caught this).

## Batch 2 — e2e portability (`f0c2551`)

`tests/e2e_mvp1.sh` used GNU-only `stat -c '%a'`, `seq`, and fractional `sleep`.
Any of them aborted the run, and a missing `python3` aborted it too: the Ollama
block called `python3` bare, so on a machine without it the suite reported
nothing at all rather than reporting a skip.

Now: `find -perm 600` instead of `stat -c`, no `seq`, integer sleeps only, and
`have_python3` gates the one block that needs it. Skips are counted and printed
instead of aborting, so a partial environment is visible in the summary line.
Verified both ways: full PATH `pass=122 fail=0 skip=0`, stripped PATH
`pass=98 fail=0 skip=17`.

## Batch 3 — zoekt plain-dir walk (`0b2a6d5`)

Two hangs/wrong-bytes bugs and one false doc, all in `IndexDir`:

* Every non-directory entry was read with `os.ReadFile`. A FIFO blocks that
  read forever, and the `ctx` check sat *before* the read, so nothing could
  interrupt it: one stray pipe in a registered plain dir hung `tk index`
  permanently. Now only regular files are read, which also matches CBM's
  "symlinks always skipped" — tk was indexing a symlink's target bytes under
  the link's path.
* Every file was slurped whole and only then rejected by zoekt's 2 MiB
  `SizeMax`, so a large file cost its own size in RAM on every index, sync and
  search. Oversize files are now skipped before the read, recorded as
  `SkipReasonTooLarge` so the skip shows in the index instead of vanishing.

**The doc correction is the part worth keeping.** Four places claimed the text
index honors `.gitignore`. It does not, at this pin: zoekt's git indexer reads
only `.sourcegraph/ignore`, and `.gitignore` applies *implicitly* — ignored
files are untracked, so they are absent from the commit tree it walks. The
consequence is a real divergence: a **committed** `node_modules/`, `dist/` or
`*.min.js` is skipped by CBM and still indexed by tk, and plain dirs get
neither `.gitignore` nor `.cbmignore` at all.

Not reimplementing CBM's filter chain inside tk. `TestIndexDirDoesNotReadGitignore`
pins current behavior so a future well-meaning "fix" fails loudly against
AGENTS.md 1 rather than silently duplicating the engine's filters.

## Batch 4 — memory guard depth (`8aba1b5`)

Batch 1 of the previous pass (2026-09-25, item 3) added `Store` guards and
claimed the nil-panic was fixed. The guards compared the *pointer* to nil:

```go
if s.Facts == nil { return errFactsNil }   // passes for &Facts{}
```

`&Facts{}` has a nil `*sql.DB`, and `database/sql` has no nil-receiver guard,
so every method plus `Close` panicked inside `ExecContext` on `db.mu.Lock()`.
The ledger was worse than a panic: `&Ledger{}` has an empty `dir`, `path()`
returns a relative name, and `Append` quietly created `<project>.jsonl` in the
process working directory.

The guards now check the handle (`db` non-nil, `dir` non-empty) and the
receiver, and `LedgerAppend` checks the receiver before the `ledger.enabled`
gate — reading `s.LedgerEnabled` on a nil `s` is the panic the chain exists to
prevent. Reachable only by a caller that builds a `Store` by hand; the MCP
path constructs it from `Open*`, which returns non-nil or an error. This is the
documented contract finally holding, not a live-fire fix.

**The general rule:** `if x.Ptr == nil` is a check on the pointer, not on
readiness. Readiness lives one level down, and that is the level to check.

## Batch 5 — MCP cancellation and notifications

`Serve` had two exits, and only one of them was real. It looped on
`bufio.Scanner`, so it ended on EOF and on nothing else: `main` wired
SIGINT/SIGTERM into the root context, `handle` passed that context down, and
for an idle server it changed nothing, because the loop was blocked in
`read(2)` on a pipe the client would never close. A Ctrl-C'd `tk mcp` stayed
alive.

The reader now runs on its own goroutine feeding a channel, and the loop
selects on `ctx.Done()`. Closing the reader to unblock it was rejected: stdin is
`os.Stdin`, a descriptor tk does not own. Each request also gets a
`requestTimeout`-bounded context, so a wedged CBM child cannot hold the loop
open, and `Server.EnsureIndex` takes a context so a cancelled `source_search`
does not leave a refresh running behind the client's back. EOF stays exit 0;
cancellation returns 1, and `tk mcp` deliberately does not propagate that — a
client that signalled us is not owed a "context canceled" on stderr.

`main` now calls `stop()` as soon as the context is done, so a *second* Ctrl-C
kills. `signal.NotifyContext` keeps its handler installed until `stop()`, so
without this a command wedged in something that ignores the context was
unkillable without SIGKILL.

Notifications are now silent. `handle` answers with a `(resp, reply)` pair and
`Serve` writes only when `reply` is set, so a `notifications/*` message — which
carries no id — gets no frame. Previously `notifications/initialized` drew a
result and anything else under that prefix drew a null-id "method not found",
both responses to messages the client marked as notifications.
`notifications/cancelled` is honored structurally: every request runs on a
context derived from the server's and the CBM spawn dies with it. Cancelling a
request that has not been read yet is not tracked — no per-request registry,
and for a one-message-at-a-time stdio server that is the right trade.

## Consequences

* `notifications/initialized` no longer gets a reply. Correct per spec, and no
  client depended on it (nothing in-repo asserted one).
* `Server.EnsureIndex` changed signature. Internal-only; one caller.
* `Serve` returns 1 on cancellation. Tests and library callers see it;
  `tk mcp` does not, by choice.
* `.gitignore` is still not honored by the text index. Now documented in three
  places instead of claimed in four.
* An oversized plain-dir file is now visible as a skip rather than silently
  absent.
