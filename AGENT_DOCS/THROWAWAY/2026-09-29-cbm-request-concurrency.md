# Can a CBM MCP child serve concurrent tools/call requests in parallel?

**Status: measured.** CBM 0.11.0, Linux, 12 cores, 2026-09-29. Repo: this one,
187 files, `tkdev`. Method and probe: below; the probe is disposable and is not
part of the harness.

## Answer: yes, and the degree depends entirely on the tool

A single child pipelines requests and does not serialise them. The gain is
**not** uniform, which is the part that matters:

| tool | serial ms/req | pipelined ms/req | speedup |
|---|---|---|---|
| `search_code` (zoekt text) | 10.91 | 0.84 | **12.97x** |
| `dead_code_detection` | 10.79 | 1.30 | **8.30x** |
| `search_graph` n=64 | 18.47 | 7.38 | 2.50x |
| `search_graph` n=128 | 17.75 | 6.32 | 2.81x |
| `get_architecture` | 22.87 | 11.41 | 2.00x |

Text search nearly saturates 12 cores. Graph search tops out around 2.8x, so
the graph path has internal contention that request-level concurrency does not
fix. `get_architecture` is worse still.

So: the transport is concurrent, and the engine underneath is only sometimes
able to exploit it. A resident session does **not** need a child pool. Whether it
needs a concurrent *client* depends on the shape of the resident — see the
correction below.

## Method

One child, one `jsonrpc` pipe, ids `1..N`. Two arms, same process, warmed
separately:

* **serial** — send one, await its reply, repeat.
* **pipelined** — send all N without awaiting, then drain N replies.

**Method bug worth remembering, because it produced a fake answer first.**
Version 1 read replies in N goroutines, one per request, each calling
`json.NewDecoder(r).Decode()` on the *same* `bufio.Reader`. That is a data
race. It reported a plausible-looking 2.4x "speedup" with `errors=3 of 4` and
`errors=7 of 8` — the tell was that the error count was always `N-1`. The
correct shape is **one** reader goroutine for the session's life, demuxing
replies to per-id channels. With that fixed, `errors=0` at every N.

A second, quieter bug: the shell helper took `(tool, n, args, label)` and was
called with `(tool, args, n, label)`, so `TK_N` received a JSON string, `Sscan`
failed, and N silently defaulted to 1. Five runs printed a "speedup" of ~1.0x
that measured nothing at all. A concurrency benchmark that degenerates to N=1
still produces plausible output.

## So what

**The measurements stand. The conclusion below is corrected on 2026-09-30.**
Corrected items 1 and 3; item 2 was always right.

1. ~~**Replies arrive out of order.** ... Demux by id. This is a correctness
   requirement~~ — **FALSE for the tk design.** Out-of-order replies are real
   CBM behaviour, but only if a client sends several requests before awaiting.
   `internal/mcp.Server` serves one message at a time, so a resident built on it
   has exactly **one** request outstanding and there is nothing to demux. The
   8-13x is unreachable through tk's server; that is a throughput trade, not a
   correctness bug, and the fix is not demultiplexing.
2. **The pool is not needed** for concurrency. One child gives 8-13x on text
   and ~2.8x on graph, on 12 cores. A pool of children would multiply store
   handles and memory to buy parallelism the engine already provides. Revisit
   only if graph-query contention is measured to hurt a real workload.
3. ~~**Backpressure is about in-flight count, not a request queue.**~~ —
   **MOOT.** With one request in flight there is no burst to bound. A resident
   built on `internal/mcp.Server` needs no in-flight cap, no queue, and no
   per-connection state beyond the single reply. Anything larger is a different
   design and needs its own measurement.

The corrected decision is
`AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md`.
**Keep the table; discard the "must demux by id" claim as a tk requirement.**

## Caveats

* One machine, one build, one small repo. 12 cores; a 4-core box would cap the
  text-search figure lower and change the table's shape.
* Latency per request here is ~10-23ms, not the ~74ms measured on TensorFlow.
  The concurrency *ratios* should survive, but re-measure on a big graph
  before quoting the speedups as representative.
* Pipelining tests dispatch concurrency, not correctness under it. No CBM
  crash, no cancellation, no partial-failure test.
* Measured against a raw MCP child, which is what a resident would hold. The
  corrected finding is that `internal/mcp.Server` never issues concurrent
  requests, so the pipelined arm has no tk-side equivalent today.
