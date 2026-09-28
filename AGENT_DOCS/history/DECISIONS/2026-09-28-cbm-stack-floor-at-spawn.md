---
title: CBM spawns get an 8MB stack floor at spawn time
status: authoritative
date: 2026-09-28
supersedes: null
superseded-by: null
---

# CBM spawns get an 8MB stack floor at spawn time

## The gap

CBM is a native C engine. Its longest-lived pipeline passes — the
`configlink` chain over every recorded symbol across the merged graph — recurse
deeply, and on hosts where the *main thread's* stack is thin they overflow.
macOS ARM64 gives new processes a default soft `RLIMIT_STACK` of 512KB;
FreeBSD-style environments and tightened launchd `/System/Library/LaunchAgents`
policies pass the same thin limit in. An overflow aborts the pass mid-run:
the daemon shows `Thread 0 crashed: EXC_BAD_ACCESS` and the index is left
partial, but the caller sees a successful `status: indexed` because the index
step never waits on the pass.

Upstream root cause: [issue #139](https://github.com/DeusData/codebase-memory-mcp/issues/139),
fixed by raising CBM's own thread stacks to an 8MB default
(`CBM_DEFAULT_STACK_SIZE = 8MB`). That fix protects CBM's *worker threads*;
the *main thread*, which the OS sizes from the process's `RLIMIT_STACK`, is
still handed the value the spawning ancestor inherits. When tk spawns CBM
from a host that already runs tight, the engine starts with a
512KB main thread and the overflow happens inside tk's own index step.

## The context

tk is a shipper. It is the thing that spawns CBM, and it is the only thing
that knows what it is spawning. The boundary rule "one spawn wrapper" is
already frozen: `AGENT_DOCS/history/compatible-implementation-spec.md` §790
requires that where the engine needs an increased thread stack, the wrapper
applies the limit to both normal CLI calls and persistent daemon startup.

Go gives tk everything it needs except one thing: there is no pre-exec hook
for an rlimit. Current `os/exec` offers no `SysProcAttr` rlimit field on any
platform, so a program cannot `setrlimit(RLIMIT_STACK)` between `fork` and
`exec`. The wolf is not at the door of the spawn; it is at the door of *who
launches the wrapper*.

That has to be a shell. `ulimit` is POSIX, it is already on every host tk
deploys to, and `ulimit <n> && exec "$0" "$@"` raises the child *soft* stack
to the inherited *hard* value and then replaces the shell with the real CBM in
the same process, preserving PID, argv, and stdio. The guard is scoped to the
child: the wrapper never changes its own caller's limits.

## The decision

`internal/cbmexec` applies a stack floor of 8MB — the same value as
Upstream's `CBM_DEFAULT_STACK_SIZE` — to every CBM spawn on Linux and macOS:

1. Read the parent's `RLIMIT_STACK` (soft, hard).
2. If the soft limit already meets the floor, exec CBM directly — the spawn is
   byte-identical to today's, so Linux CI and default-bash developers see no
   change at all.
3. If the soft limit is below the floor, rebuild the spawn through
   `/bin/sh -c 'ulimit -s <hard> && exec "$0" "$@"'` with the hard limit in KB
   (or the literal `unlimited`). The raise is capped by the hard limit: a
   parent whose *hard* limit is also thin gets its max (which is
   the only honest result possible). The soft limit is never lowered on any
   path.
4. The guard applies to all four spawn sites through the single
   `commandContext` helper: normal calls, envelope calls, raw passthrough
   (`tk cbm`), and persistent caching (install-less) startup. Nothing is
   made conditional on the tool name; the daemon runs the same pipeline.
5. Windows has no `RLIMIT_STACK`; its guards are no-ops.

Bash footgun the tests had to route around: `ulimit -s 512` in bash lowers
*both* soft and hard to 512 — a legitimately thin hard cap, in which case the
raise is impossible by design and the guard honestly reports the crippled
ceiling. The e2e cripples cases with `ulimit -S -s` so only the soft limit is
thin and the raise to the hard limit can actually be observed.

## What this costs

A `fork+exec` goes through `sh` only on the rare hot path (a thin parent).
Each such raise also costs the C engine nothing once the pipeline is over
thread creation; 8MB of address space is reserved, not committed, so the VM
footprint is unchanged. The `-s` value is derived from the *inherited hard
limit* and never raises the hard ceiling — tk deliberately does not privilege
itself.

Both intentional exclusions from the guard are documented:

* `tk`'s own Go runtime does not need 8MB; its stacks are heap-scoped. The
  floor applies to the C engine spawn, not to tk itself.
* `installer.DetectVersion` runs `<bin> --version` with a scrubbed environment
  and no pipeline: a single `main()` that prints a version and exits. It is
  not next to the overflow class, and route-through-`sh` would add an
  apparent privilege step to every install; it stays direct.