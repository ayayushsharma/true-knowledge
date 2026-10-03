# Do tk reads actually cost 4.9s, and can an extension's nudge ride in-band?

**Question.** Two that were open: is `tk mcp` really ~4.9s per graph call, and
can an injected note reach the model in the request already in flight, with no
extra turn?
**Status.** measured — both. CBM 0.11.0, tk 0.1.0, `TK_HOME=/tmp/tk-probe`,
project `tkreal` = this repo (230 files, moderate). 2026-10-03.
**So what.** The resident dial in `internal/mcp` is the single biggest latency
change available, and it is worth ~130×, not the ~2× the cold numbers suggest.
The nudge has an in-band channel and does not need the projection fallback.

## The engine, actually installed

`tk install cbm` works on this box: 38 MiB download, 716ms manifest, sha256
verified, `cbm 0.11.0` published to `$TK_HOME/cache/bin/`. `tk index tkreal`
then took **7.3s** for the graph pass and 189ms for the text pass. Everything
below is real engine time, not a fail-open hint.

## Latency, measured through `tk mcp` (stdio, one process, 5 calls each)

| call | cold (first run) | warm spawn, resident up but undialed | socket dial (CLI) |
|---|---|---|---|
| `search_graph` hit | **4911ms** | **2008ms** | ~15ms |
| `search_graph` empty | **9405ms** | — | ~26ms |
| `get_architecture` | **5406ms** | **1900ms** | ~12–19ms |
| burst of 3 graph calls | **10370ms** | **5356ms** | ~50ms |

Baseline `tk --version` is 30.4ms, so the "socket dial" column is process
startup subtracted — that is the engine time over the socket.

Three corrections to the record, all of them load-bearing:

1. **4.9s is the cold number.** With the page cache warm — which a running
   resident guarantees even for a client that spawns — a spawn costs **2.0s**,
   not 4.9s. `AGENT_DOCS/THROWAWAY/2026-09-29-cbm-one-shot-latency.md` records
   the cold figure. Both are real; only the cold one is representative of a
   first call, and neither is representative of steady state.
2. **The historical 74ms socket figure is pessimistic.** Measured engine time
   over the socket is 12–26ms here. Different machine, 0.11.0.
3. **An empty `search_graph` really is two spawns** — 9405ms against 4911ms for
   a hit, matching `callStructured` running a coverage probe first. Over the
   socket that becomes two round trips, 26ms total. So the empty case is the
   *best* argument for P1 and the worst case for its absence.

Argument validation is local and free: a `source_search` call with the wrong
parameter shape returned a `-32602` in **0ms**.

**What this settled, and what happened next.** `tk mcp` never dialled the
resident: `internal/mcp/server.go` built no resident client, and the only dial in
tk lived in `Ctx.residentTry` in `internal/cli/root.go`. So while the resident
served CLI reads in 15ms, the MCP server still paid 2008ms — same machine, same
index, same engine. The gap was structural, not incidental, and it made Phase 1
the precondition for every latency claim downstream.

Phase 1 shipped that dial on 2026-10-03, and the product re-measurement is the
same story from the other side: `tk mcp` over stdio with a resident up gives
`search_graph` **16.6ms** median and `check_index_coverage` **14.8ms**, both
`backend: resident` in `tk.log`. Kill the resident, same calls: **4411ms** and
**4407ms**, `backend: spawn`, byte-identical payloads (424B / 1447B). The
resident changes the time and nothing else. See
`AGENT_DOCS/history/DECISIONS/2026-10-03-mcp-dials-a-running-resident-before-it-spawns.md`.

## Profile sizes, measured from `tools/list`

| profile | tools | schema bytes | ~tokens (÷4) |
|---|---|---|---|
| `minimal` | 3 | 1730 | 432 |
| `scout` | 11 | 5435 | 1358 |
| `analysis` | 15 | 7198 | 1799 |
| `memory` | 22 | 9836 | 2459 |

`analysis` is **15**, not the 14 `AGENT_DOCS/04-MCP.md` states; `get_graph_schema`
(`internal/mcp/server.go:258`) is the extra one. The doc is wrong, not the code.

## Memory reads are cheap on both paths — and MCP is 30× cheaper

`TK_MCP_PROFILE=memory`, one process, median of 7:

| call | via MCP | via CLI spawn |
|---|---|---|
| `note_toc` | **0.46ms** | 35.1ms |
| `note_search` | **0.62ms** | 32.9ms |
| `ledger_get` | **1.55ms** | — |
| `get_architecture` (no engine) | 0.50ms, fail-open hint | — |

So the extension's per-turn probe is **~2.7ms** over the MCP connection it
already has open, against ~100ms for three CLI spawns. Decision: **read memory
through MCP; keep the CLI for `tk sync` only**, which is CLI-only today and
carries its own quiesce semantics. `tk.ts` shrinks to one function.

Tool-name details the nudge depends on: `mem_recall` takes `topic`, not `query`
(`topic must not be empty`); `source_search` takes `pattern`; `tk find` is
`--query`, never positional (`unknown command "residentTry" for "tk find"`).

## The in-band channel exists, and it is better than the deferred one

Read in `dist/core/agent-session.js`, pi 1.0.0:

* `sendCustomMessage` checks `deliverAs === "nextTurn"` **first**, before the
  streaming and `triggerTurn` branches, and pushes to `_pendingNextTurnMessages`.
* In the prompt path, `emitBeforeAgentStart` runs at ~line 1554; the user message
  is built next; then pending `nextTurn` messages are pushed at 1575-1578; then
  the handler's **own returned `result.messages`** are pushed at 1579-1588 as
  `role:"custom"`; then `_runAgentPrompt(messages)`.

So a nudge raised in `before_agent_start` lands in the request already in
flight, after the user prompt. No probe extension needed to find that out.

**Use the handler's return, not `sendMessage`, for the nudge.** It is the same
request, with deterministic placement and no queue. `sendMessage(nextTurn)` is
the right channel for something discovered *outside* a turn — which gives
`agent_end`'s blast radius a home: queue it, and it becomes next turn's nudge,
with no per-turn context at all.

Both are persisted: any `role:"custom"` message that flows through the agent
emits `message_end`, and `agent-session.js:741-743` turns that into
`sessionManager.appendCustomMessageEntry`. The transcript shows what was
injected, so the nudge is auditable rather than a trick.

## Extension-registered servers do get the `mcp_servers` section

From `dist/extensions/mcp/index.js`:

* `servers = [...loaded.servers, ...registeredServers()]` — a
  `pi.registerMcpServer` entry is in the same list, so it is in the section.
* `pi.on("before_agent_start")` re-renders the section every turn into
  `sections.mcp_servers`. Content-stable, so the prefix cache still holds.
* `exposureOf(entry) = entry.config.exposure ?? "codemode"` — **the default is
  codemode, not deferred.** The extension must set `exposure` explicitly.
* For `exposure:"deferred"` the whole always-on cost is two lines:

  ```
  MCP servers whose tools are not declared to you. Load the tools of `tool_search` servers with `tool_search`.
  - mcp__tk (tool_search): <summary>
  ```

  ~40 tokens, not 22 schemas. That is the strongest measured argument for
  deferred exposure with a 30B.
* The summary is **first line of `config.description`, falling back to
  `connection.instructions`** — the config description wins, so the extension
  should set it deliberately.
* `MAX_SERVER_DESCRIPTION_CHARS = 250`, `MAX_SERVERS_SECTION_CHARS = 4096`, and
  the shrink is a character slice with `…`. Keep that first line under 250
  characters or it is cut mid-word.

## `initialize` carries no `instructions` today — verified on the wire

`initialize` returns exactly `capabilities`, `protocolVersion`, `serverInfo`
(`{'name':'tk','profile':'memory','version':'0.1.0'}`). There is no
`instructions` key. pi would use one if present, as a description fallback.
Adding a single short first line is therefore free for every client that reads
it, and pi's section gets it only when the extension sets no `description`.

## What compounds are worth after P1

`find` and `explain` were justified partly on latency. Measured, that
justification dies: after the dial, three separate calls cost ~45ms of engine
time, and what the compound really saves is *model round trips and output
tokens*. It still earns its place for a 30B — one tool call instead of three is
three fewer chances to mis-call — but it is an output-performance change, not a
latency change, and the ADR should say so rather than borrow P1's numbers.

## Dead ends and traps

* `tk find <query>` positional is wrong; `--query` is required. `tk trace
  --query` does not exist at all. Both fail fast, so a typo looks like a fast
  success in a timing loop — check the output, not the clock.
* `tk note list` prints the parent help; the real path is
  `tk note review list`.
* `note save` lands in the review queue and `note search` cannot see it until
  `note review approve <id>`. A probe that never fires against fresh notes is
  working correctly.
