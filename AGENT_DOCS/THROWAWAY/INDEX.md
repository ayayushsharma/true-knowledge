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
| Does the resident pid-file test flake under `-race`? | `2026-10-02-resident-pid-file-test-race.md` | **measured: yes**, ~1 in 8, pre-existing |
| Where should a pi extension's per-turn recall run: in the model loop or in the harness? | `2026-10-03-pi-extension-recall-in-the-harness.md` | design; `tk mcp` spawns per call (code-verified), so harness reads go over the CLI |
| What is the build order for the resident dial, the MCP compounds, and the pi extension? | `2026-10-03-tk-pi-plan.md` | design; P0 measure, P1 resident dial, P2 compounds, P3 extension, P4 docs |
| What do tk reads really cost, and can a nudge reach the model without an extra turn? | `2026-10-03-measured-latency-and-pi-probe-facts.md` | measured; 2.0s warm spawn vs ~15ms socket, empty `search_graph` 9.4s, in-band channel exists |
| Which in-loop tk calls fit under 100ms, and what do the gates and `/` commands look like? | `2026-10-03-latency-budget-and-operator-surface.md` | measured; 10 of 12 tools 14–30ms over the socket, `detect_changes` 591ms and `find`'s regex route 151ms are the two that break the target |
| Is the pi extension buildable, and what does it do in an unregistered directory? | `2026-10-03-tk-pi-prototype.md` | planned; five-state capability probe, memory needs no registration or CBM, ledger works for unregistered names, `source_search all_projects` is the cross-repo path |

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

## Four ways to get a wrong design

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
* **"The client holds the process open" read as "the client's calls are warm."**
  `tk mcp` is a stdio proxy that runs for a whole session and still spawned the
  engine per tool call: `internal/mcp` had no dialer, and `Ctx.residentTry` lived
  only on the CLI read path. A long-lived MCP connection is not a warm transport,
  and designing a per-turn recall fan-out on top of one cost ~4.9s per call
  instead of ~74ms. Check which dialer a path actually calls. **Resolved
  2026-10-03:** the MCP server dials first now
  (`history/DECISIONS/2026-10-03-mcp-dials-a-running-resident-before-it-spawns.md`),
  measured 4411ms → 16.6ms with identical payloads. The lesson did not expire
  with the bug — a spawn is still 2000ms wherever no dialer reaches.

* **A constructor's doc comment read as a directory walk.** A throwaway note
  claimed `NewDirectorySearcher` loads every shard *below* a directory, so one
  searcher already spanned repos and the fleet was one hook. It globs
  `<dir>/*.zoekt` — flat. Against tk's `<cache>/zoekt/<project>/` layout a
  parent-dir searcher returned 0 matches and **no error**, so the design would
  have shipped a fleet search permanently answering `(no matches)`. A `repo:`
  filter cannot rescue it: there is no loaded query to attach one to. The tell
  was citing `shards.go:251`, the constructor line, instead of the glob in
  `watcher.go:141` that the constructor calls. Landed as
  `history/DECISIONS/2026-10-02-cross-repo-fleet-text-search.md`.

