---
title: tk owns the bytes, the swap, and the daemon it must stop
status: authoritative
date: 2026-09-28
supersedes: [AGENT_DOCS/history/DECISIONS/2026-09-28-delegate-backend-install-to-vendor.md]
superseded-by: null
---

# tk owns the bytes, the swap, and the daemon it must stop

## What the superseded ADR got wrong

Delegating to the vendor's `install.sh` was right about one thing and wrong about
another. It was right that a warm daemon holds the old image and that a bare
`os.Rename` over a live target is unsafe. It was wrong that tk could not own the
swap, because the job it delegated was not the job it actually needed.

The delegated script does four things: download, verify a checksum, extract, and
then call the candidate's own `install`, which drains sessions and swaps. tk
needed the first three and a different fourth. Delegating bought the drain as a
side effect, and paid for it with an opaque exit status, a `~/.zshrc` write that
needed a sandboxed `HOME` to contain, and a 300 MB transfer tk could not see.

The superseded ADR also kept the claim that tk "never signals a daemon on its
own". That claim was doing no work: it was a consequence of delegating, not a
boundary anyone had weighed. A boundary is a reason. This ADR supplies one.

## What the source actually says

Read at v0.11.0 rather than inferred, because the design depends on it.

* The rendezvous namespace is `CBM_RUNTIME_DIR` plus uid. The key is a constant
  FNV hash of a fixed domain string — it excludes version, build, path, and cache
  root on purpose (`service.h`, `service.c`, `ipc.c`). tk points it at
  `<state>/rendezvous`, so a tk daemon is per-`TK_HOME` and cannot be an
  account-wide daemon a consumer laptop is running.
* Control-plane operations — `daemon status`, `daemon stop` — are dispatched
  *ahead of* the HELLO build comparison (`runtime.c`). A different build can
  therefore stop the running daemon. Confirmed with a 0.10.8 binary retiring a
  0.11.0 daemon.
* Session admission is the opposite. HELLO compares semantic version first, so a
  new build against an old live daemon is refused with a conflict. The old
  binary keeps its inode, the new binary cannot connect, and every later command
  fails on a machine that reports itself upgraded.
* `daemon stop` is refuse-if-busy, not force (`runtime.c`). With a committed
  client it names the pids, stops nothing, and exits nonzero.

## Decision

tk downloads, verifies, extracts, and swaps the binary itself. Before the swap,
when the install will actually replace a tk-managed binary, `tk install` stops
the daemon once and says which pid it stopped. A refusal is a failed install:
the pids are printed and the binary is left alone.

`tk install` never forces. There is no flag that can, because the refusal is the
daemon's policy and the only mechanism that drains a committed cohort is CBM's
activation barrier, which lives behind `cbm install` and is not tk's to call.

No `tk update` command. `cbm_version_pin` stays the only CBM version authority,
and it is a compiled-in default or a config value — never a `latest` lookup.

## The swap

Checksum verified before any write, then staged beside the target: extract to
`<dest>.new`, validate the staged file reports the pin, retain the old file as
`<dest>.prev`, publish, re-validate, and roll back to `.prev` on any failure.
One retained generation means a failed update leaves a working binary, and
re-running an older pin is the rollback.

`DetectVersion` runs the probe with the CBM identity variables scrubbed. A PATH
copy must not be able to redirect its own store by being asked its version.

## Delivery of tk itself

A repo-root `install.sh` installs tk and then calls `tk install cbm`. The script
exists because tk cannot replace its own running binary: on Windows the image is
locked, and on Linux the running inode survives the rename. An external process
has neither problem.

The script holds no CBM version input. `DefaultCBMPin` stays authoritative, so
there is one place where the CBM version is decided. This couples tk's release
to the CBM pin, which is a real coupling and is now documented as one.

## What this costs

On a machine where any `tk mcp` session is live, `tk install --update` fails.
That is correct: the alternative is a binary that cannot be admitted by its own
daemon. The workflow is close the sessions, update, reopen, and the script makes
that one command.

`.tk-versions.json` is gone. The config pin is the source of truth, and a second
ledger could only drift from it.
