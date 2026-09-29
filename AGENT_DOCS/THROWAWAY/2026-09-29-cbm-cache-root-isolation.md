# Do tk and an account-wide CBM installation fight over the cache root?

**Status: measured — no, they do not.** 2026-09-29, CBM 0.11.0, Linux.

## Question

Upstream `README.md` says:

> All active CBM processes must run the exact same version, executable build,
> coordination ABI, and canonical cache root. Equivalent `CBM_CACHE_DIR`
> aliases resolve to the same root; **a genuinely different root is rejected
> while any CBM process is active.**

tk sets `CBM_CACHE_DIR` to `<TK_HOME>/cache`, which is a genuinely different
root from the account default `~/.cache/codebase-memory-mcp`. If the
documented rejection applied across roots, then running CBM in an editor
*and* tk would fail — which would look like random breakage.

## Test

Held an account-default-root CBM session open (`sleep 45 | cbm` with
`CBM_CACHE_DIR=$HOME/.cache/codebase-memory-mcp`), confirmed 2 live processes,
then ran `tk find` against the isolated `TK_HOME` root.

## Result

The tk command **succeeded**, 4782ms, and **no conflict was recorded** in
either root's `logs/daemon-conflicts.ndjson`.

## Why, most likely

The admission barrier lives in `$CBM_RUNTIME_DIR/cbm-daemon-<uid>/`. tk sets
`CBM_RUNTIME_DIR` to its own state directory, so the two installations have
**separate barriers and never observe each other**. The documented
cache-root rejection is scoped to processes sharing a runtime dir.

This is an inference from one test plus the lock layout, not from upstream
docs — the README does not state the scoping.

## So what

When adding a resident session, do not expect cross-installation conflict
handling to be a problem. Do expect the flip side, which is a product
decision rather than a bug:

* tk's graph store is **separate** from what the user's other agents see.
* Indexing happens **twice** — once account-wide, once into `TK_HOME` — unless
  the user points both at one root.

That duplication is the price of rule 3's isolated homes. Worth knowing before
someone reports "tk does not see my other agent's index."

## Caveat

One test, one direction, no simultaneous same-root contention. If a resident
session is built, retest with the resident also alive.

## Corrected 2026-09-30 — the unit is the home, not the process

The test above is still valid and the conclusion still holds, but "tk's graph
store is separate" was imprecise and would mislead a resident design. Measured:

```
TK_HOME=/tmp/opencode/tkhome     -> tkhome/cache/probe.db
TK_HOME=/tmp/opencode/tkhome2    -> tkhome2/cache/probe2.db
~/.cache/codebase-memory-mcp/    -> _config.db only, no project db
```

**One cache root per tk home, shared by every tk process under that home.**
Every `tk` invocation resolves the same `Paths.Cache` and exports it as
`CBM_CACHE_DIR`, so a resident's child, `tk index`, and the watcher all read the
same `<cache>/*.db`. CBM keys its cohort on the canonical root (`main.c:1465`,
sha256 of the canonical cache path), which is why aliases resolve together.

So "separate" means **per `TK_HOME`**, not per process. The safety property that
makes a resident safe is the *shared* root within a home, not isolation between
processes. An earlier draft called it "tk's own cohort" and reasoned about
cross-cohort races that cannot happen inside one home. Corrected in
`AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md`.

For the "do not see my other agent's index" case: still true across the tk home
vs the account root, still a product decision, still the price of rule 3.
