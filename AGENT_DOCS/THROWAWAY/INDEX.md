# Throwaway index — read before you search

Newest last. Status is the point: `measured` you can quote, `inferred` you
must verify, `dead-end` do not walk again.

| Question | Note | Status |
|---|---|---|
| Why does a `tk` read take ~5s when the engine claims <1ms queries? | `2026-09-29-cbm-one-shot-latency.md` | measured |
| Does a warm CBM MCP child serve stale results after another process reindexes? | `2026-09-30-warm-child-store-freshness.md` | **measured: no** |
| Does the persistent CBM MCP child return the same payload shape as `cli --json`? | `2026-09-29-cbm-mcp-envelope.md` | measured |
| Can one CBM MCP child serve concurrent requests in parallel? | `2026-09-29-cbm-request-concurrency.md` | measured (yes; but tk is serial) |
| Do tk and an account-wide CBM installation fight over the cache root? | `2026-09-29-cbm-cache-root-isolation.md` | measured (no) |
| How does a resident session survive tk upgrades, CBM binary swaps, and concurrent queries? | `2026-09-29-resident-session-lifecycle.md` | inferred (design) |
| Can tk build for Windows at all, or is a `Setsid` platform split wasted work? | `2026-09-30-windows-build-blocked-upstream-by-zoekt.md` | **measured: no, blocked by zoekt** |
| Which lock is the 1ms poll actually on, and is it contention or a fixed retry budget? | — | **unanswered**, see the latency note |
| Is 74ms the floor for a warm query, or does a warm store do better? | — | **unanswered**, see the latency note |

## The one-line version

A read command spends 4.9s in CBM's per-command admission lease and ~74ms
actually querying. The lease is documented behaviour for one-shot `cli` mode,
not a misconfiguration. A long-lived MCP child pays it once.

A long-lived child serves requests **concurrently** — 13x on text search, 2.8x
on graph — so it needs no child pool. But `internal/mcp.Server` sends one at a
time, so tk does not exploit that and needs no id demultiplexing.

A warm child is **not stale**: it re-resolves the store per call, so a resident
needs no poller, watcher, or generation counter. That killed more design than
it enabled.

## Three ways to get a wrong benchmark

All three hit this session and all produced output that looked fine. Recorded
because the failure is silent, not because the numbers mattered:

* **N concurrent `Decode` calls on one `bufio.Reader`** is a data race. The
  tell was `errors=N-1`. Correct shape: one reader goroutine, demux by id.
* **An argument-order bug that made N default to 1.** A concurrency benchmark
  that never sends concurrent requests still prints a speedup of 1.0x and looks
  like a real negative result.
* **A label/argument mismatch in the freshness probe** that printed the wrong
  symbol name next to a correct query, which is how a passing result got
  re-read as a failure once.

## Three ways to get a wrong design

All three were confident, evidence-backed, and wrong. The general shape: a real
quoted fact, read past its scope, and a mechanism invented to defend against it.

* **A scoped exception read as a global invariant.** CBM's `README.md:697` warns
  that `manage_adr` query modes can return a pre-publication result until idle
  eviction refreshes the cache. Read as "long processes serve stale data", it
  produced a poller, an inode-comparison scheme, and a generation counter.
* **Absence of a mechanism read as absence of behaviour.** "CBM exposes no
  staleness API" is evidenced; "CBM serves stale data" is a different claim and
  was not.
* **The wrong file treated as the store.** `05-INDEXING.md:155-156` describes the
  shareable export, which reads exactly like the query store. The queries open
  `<CBM_CACHE_DIR>/<project>.db`.

