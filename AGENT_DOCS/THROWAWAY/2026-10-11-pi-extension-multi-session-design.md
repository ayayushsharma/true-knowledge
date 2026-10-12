# pi extension × tk lifecycle × N concurrent sessions — design

Date: 2026-10-11. Status: design (proposed, not built).
Companions: `2026-10-03-tk-pi-plan.md` (phases), `2026-10-03-tk-pi-prototype.md`
(executable spec), `2026-10-03-latency-budget-and-operator-surface.md` (SLO,
gates, `/` surface). Those notes stand; this one answers the question they
left open: **what changes when N pi sessions share one tk.**

## What changed since the Oct-03 notes (all verified today)

* The resident dial shipped in `internal/mcp` (ADR 2026-10-03): MCP calls cost
  ~16ms warm, ~4.4s cold, byte-identical payloads.
* Resident `dropEmptyStrings` (ADR 2026-10-11): resident and spawn now answer
  the identical question; parity is structural, not hoped-for.
* Resident serve path is concurrent-safe: `go s.serveConn(conn)` per
  connection, one request per connection, engine driven one-at-a-time via
  `s.one` (code comment cites the 19.5s silent-peer outage that motivated it).
* Current pi docs (not the 1.0.0 bundle): factory must NOT start
  processes/sockets/timers (some invocations load extensions without a
  session); start session resources from `session_start`, close from an
  idempotent `session_shutdown`. Tool calls from one assistant message can
  run in parallel. Server `exposure` default is `codemode`.

## Architecture: one resident, N sessions, zero shared mutable state

```
pi session A ── tk mcp (stdio, pi-owned) ──┐
pi session B ── tk mcp (stdio, pi-owned) ──┼──▶ resident.sock ──▶ one CBM child
pi session C ── tk mcp (stdio, pi-owned) ──┘         (per TK_HOME)
```

* Each session registers its own `tk` server via `pi.registerMcpServer`
  (per-session registration, never saved; a same-named `mcp.json` entry takes
  precedence and `/mcp` shows the override). pi kills it on session end
  (stdin close → SIGTERM → SIGKILL process group); attached `tk mcp` exits
  instantly on stdin EOF. No extension cleanup needed for the server itself.
* All sessions in one `TK_HOME` share one detached resident. The resident has
  no idle timeout and dies only on SIGTERM — by design, so sessions come and
  go while it stays warm.
* Memory/notes/ledger go over each session's own MCP connection (~0.5–1.5ms,
  measured); `tk sync` stays CLI-only with its quiesce semantics.

## Lifecycle map: who owns what

| moment | owner | action |
|---|---|---|
| extension load (factory) | pi | parse env gates only. **No spawns, no sockets** — pi contract, and some loads never start a session. Invalid gate value = hard error here. |
| `session_start` | extension | idempotent `tk mcp --detach` (second caller gets `already running` + pid — the N-session start race is already solved server-side); five-state capability probe; build sections; pre-warm (`tk sync` if dirty) so the model's first `source_search` refresh is already done. |
| per turn | extension | memory-only relevance probe (gated nudge via `before_agent_start` return); record turn's tk tool names from `tool_call` (gate 3). Never the graph. |
| `agent_end` | extension | `tk sync` + `detect_changes` (600ms is free here); queue blast radius as next turn's nudge via `sendMessage(nextTurn)`. |
| `session_shutdown` | extension | idempotent cleanup of session-keyed state. **Never stop the shared resident** — another session may be mid-turn. Stop is a human act (`kill` the pid-file pid); `/tk resident status` prints the pid and says so. |
| `tk install` (any session, human-driven) | operator | quiesce → swap → resume. The extension never auto-installs. |

## Concurrency: why sharing is safe, and where it serializes

* **Engine:** `s.one` serializes calls at ~10–16ms each. A 5-session burst of
  graph calls is a short queue, not an outage. No pool, no demux — the warm
  child already serves concurrent connections (measured 13× on text search).
* **No cross-session head-of-line blocking:** `tk mcp` dispatch is serial
  *per process* (`server.go:497`), and each session owns its process. Within
  one session, a 591ms `detect_changes` still blocks that session's other
  calls — keep red tools out of the model surface (budget note stands).
* **Memory:** SQLite WAL + `busy_timeout(10000)` + `SetMaxOpenConns(1)`
  (`internal/memory/facts.go`) — concurrent cross-session reads/writes
  serialize in the driver, not in the extension.
* **No scope leak:** `project` is a per-call argument, required, never
  defaulted. Sessions in different repos share the resident and the memory DB
  but cannot read each other's graph scope; cross-project memory recall is
  deliberate (prototype's state-4 finding), not a leak.
* **Extension state:** key everything by session id, never module globals.
  Two sessions in one pi process must not share nudge rate-limiters or
  section caches. Shared actions (ensure-resident) must be idempotent —
  `runDetach` already is.
* **Freshness across sessions:** the warm child re-resolves the store per
  call (measured: no staleness). Session A reindexes while session B reads —
  B sees fresh data with no generation tracking, no watcher, no poller.

## Failure modes, each with a named behavior

* **Resident absent** (never started, crashed): every path falls back to
  spawn. Slow (~2–4s) but correct; `backend: spawn` in `tk.log` names it.
  `session_start` ensure makes this a first-session-only cost.
* **Quiesced** (install swapping the engine mid-session): reads are
  **refused as errors, never silent fallbacks** — the model sees a tool
  error, not a clean miss. Extension adds no retry loop; a one-line
  `tool_result` annotation ("backend swapping, retry the call") is the most
  machinery this deserves.
* **Stale pid file / dead socket:** `Available()` gates on a real connection
  (the detach path waits for accept, not for the file), so a dead resident
  reads as absent, not as broken.
* **Different `TK_HOME`:** different socket, different resident, different
  memory DB. Correct isolation, not a bug — but `/tk status` must print the
  effective `TK_HOME` so two terminals that disagree can see why.

## Startup probe (unchanged from prototype, one interim)

`list_projects` still lacks `path`/`state`/`cwd_project` (P2, unbuilt), so the
five-state probe still costs one engine-free `tk status --json` CLI spawn
(~30ms) at `session_start`. When the MCP fields land, `capability.ts` drops
the spawn with no other change — the five states are already specified.

## Defaults per session (D3/D4 revisited for N sessions)

* `TK_PI_EXPOSURE=deferred`: ~40 always-on tokens per session regardless of
  tool count; `tool_search` activates per turn. `direct` would multiply
  ~1400–2400 schema tokens by N sessions' contexts — the cost is per session,
  so the default matters N times.
* Profile: `scout` until `compact`/user-profiles exist (still undecided per
  backlog — this design does not settle it). Set per registration via the
  server entry's `env` (`TK_MCP_PROFILE`), so sessions can differ without
  touching global config.
* Env gates and `/tk` surface carry over from the budget note unchanged, plus
  `/tk resident status` (pid, uptime, per-tool latency from `tk.log`) and
  minus any stop verb — the extension never stops a shared resident.

## Build order (prototype's six steps, updated)

1. `capability.ts` + five-state fixtures (add: multi-`TK_HOME` fixture).
2. `sections.ts` — byte-identical re-render on unchanged inputs (prefix-cache
   safety × N sessions).
3. `nudge.ts` + four gates — zero-byte delta default; `/tk recall` prints
   gate verdicts.
4. `index.ts` — env gates, per-session `registerMcpServer`, session-keyed
   state, `session_start` ensure-resident (assert: two concurrent ensures,
   one pid).
5. `commands.ts` — `/tk status` prints effective values + provenance +
   `TK_HOME` + resident pid; no stop verb.
6. `tk.ts` — `sync` + ensure-resident only (memory reads moved to MCP per
   the 30× measurement; `TK_PI_*=0` spawns nothing).

## Acceptance (multi-session additions to the prototype's list)

* Two sessions, one `TK_HOME`: identical resident pid in both `/tk status`
  outputs; killing session A's `tk mcp` leaves B's calls at ~16ms.
* Quiesce during a two-session burst: the reading session gets tool errors
  with the swap annotation, never empty results; `tk.log` shows `refused`,
  never a silent spawn.
* Burst of 10 graph calls across 3 sessions: p99 per call under 500ms
  (serial 15ms × queue depth, no timeout).
* `project` omitted from a graph tool still fails `-32602` in every session
  (the convenience of sharing never becomes a default).
* State-4 session (unregistered cwd) alongside a registered one: full memory
  surface, no graph-for-here, no cross-talk in either direction.

## Open questions (still open after this design)

* Session-id availability in pi events for state keying (docs imply
  per-session contexts; verify against the extension types, not memory).
* Exposure/profile defaults need a 30B A/B, not a preference (D3/D4 stand).
* Whether a 30B responds to a nudge at all (`/tk recall` measures the gates,
  not the model).
* `server/discover` + 2026-07-28 ladder: unrelated trigger (a real client
  asking), not this extension.
