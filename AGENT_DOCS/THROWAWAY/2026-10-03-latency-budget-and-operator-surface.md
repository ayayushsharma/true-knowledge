# Can every in-loop tk call be under 100ms, and what has to be gated?

**Question.** Set a hard target — every call inside an agent loop under 500ms,
ideally under 100ms, chaining allowed — then measure which tk tools fit, and
design the env-var gates and the `/` command surface around the answer.
**Status.** measured — CBM 0.11.0, tk 0.1.0, `TK_HOME=/tmp/tk-bench`, project
`tkreal` = this repo (230 files, moderate), resident up. 2026-10-03. Companion
to `2026-10-03-measured-latency-and-pi-probe-facts.md`.
**So what.** The target is reachable for 10 of 12 tools with no new machinery —
the socket round trip is 10–16ms. Two tools break it, both for reasons tk does
not own, and one of them (`detect_changes`) should leave the model's tool surface
entirely. The env gates and `/` commands are specified below.

## The budget, per tool

CLI over the resident socket, median of 7, engine = wall − 29.5ms process
startup. Round trips counted from `resident.log`, which records one line per
served call.

| tool | engine | socket trips | class |
|---|---|---|---|
| `trace_path` | **14.5ms** | 1 | green |
| `validate` | **14.7ms** | 1 | green |
| `get_file_outline` | **15.8ms** | 1 | green |
| `query_graph` | **24.6ms** | 1 | green |
| `get_architecture` | **24.7ms** | 1 | green |
| `source_search` (zoekt, in-process) | **28.1ms** | 2 | green |
| `explain` (compound) | **28.3ms** | 2 | green |
| `find` empty result | **28.9ms** | 2 | green |
| `find` NL→semantic | **30.4ms** | 2 | green |
| `search_code`, via `find`'s regex route | **116ms** | 1 | amber |
| `detect_changes` | **591ms** | 1 | **red** |

**The target is not engine-bound.** One socket round trip is 10–16ms, so a
five-call compound is ~75ms and still green. Chaining is cheap; the budget is
spent elsewhere.

## Two tools break the target

**1. `detect_changes` costs 591ms inside the engine, on a clean tree.** One round
trip, `resident detect_changes ok in 591ms`, 4425 bytes of real output, exit 0.
There is no diff to map, so the cost is the engine's own work, not tk's. tk
cannot make this cheaper. Three options, and the design takes all three:

* out of the 30B's default surface — not in `compact`;
* off the model's critical path — the extension already runs it at `agent_end`,
  where 600ms costs the user nothing;
* available when asked for, in `scout` and `analysis`, with its cost in the tool
  description so the model can choose to spend it.

**2. `tk find`'s regex route calls the engine's `search_code`, not zoekt.**
`tk --verbose find --query "retry.*backoff"` prints
`resident: search_code -: answered in 116ms`, while the same pattern through
`tk grep --pattern` — trigram zoekt, in-process — costs 28ms across 2 round
trips. So the route labelled "regex→grep" spends 151ms to do something the
in-process index does in 28ms. Preferring zoekt for regex-shaped queries is a
small, local fix worth ~90ms on the single most-used compound. `04-MCP.md`
describes the routing as regex→grep, so the code and the doc disagree about
where that route goes.

## The constraint nobody costed: `tk mcp` dispatch is strictly serial

`internal/mcp/server.go:491` — `resp, reply := s.handle(ctx, req)` runs inline in
the single-reader `for`/`select` loop. No goroutine per request. Clients dispatch
concurrent tool calls; the server serves them one at a time.

Consequence for a latency SLO: **the worst tool in a turn sets the floor for
every other call in it.** A 591ms `detect_changes` issued alongside three graph
queries makes all four take 591ms. The resident serializes too, but at 10–16ms
per call that is invisible.

Two responses, in order:

* keep red tools out of the model's surface (above), which is free;
* consider concurrent reads with serialized writes, since the memory stores
  already run WAL with `busy_timeout(10000)` and `SetMaxOpenConns(1)`
  (`internal/memory/facts.go:93,113-114`). This is a boundary change — dispatch
  serialization is currently a deliberate rule, not an accident — so it is an
  ADR, and it is P2, not P1.

## One unmeasured risk, stated as a risk

`source_search` runs `EnsureIndex` before searching, which can rebuild zoekt
shards. On this clean tree it cost 28ms; on a dirty tree the rebuild happens
*inside the model's call*. **Not measured** — deliberately not induced. The
mitigation is architectural rather than clever: the extension pre-warms at
`session_start` so the refresh the model triggers is already done. Add it to the
budget as a known unbounded term until someone measures a dirty-tree
`source_search` on a large repo.

## The SLO to write down

| class | bound | rule |
|---|---|---|
| green | ≤100ms | the default expectation for every tool in `compact` |
| amber | 100–500ms | allowed, and the tool description says so |
| red | >500ms | never in the model's surface; harness-side or excluded |
| any | >500ms when it happens | the call annotates its own measured wall time, so a regression is visible in the result instead of in someone's patience |

And the precondition, which is no longer hypothetical: **before the dial every
number in the green column was 2008ms.** The dial is not an optimization here,
it is what makes the target reachable. Shipped and verified through the product
on 2026-10-03 — `tk mcp` over stdio: 4411ms → 16.6ms median with identical
payloads (`AGENT_DOCS/history/DECISIONS/2026-10-03-mcp-dials-a-running-resident-before-it-spawns.md`).

## Env-var gates

Precedence mirrors tk's own chain — explicit flag, env, config, default — with
runtime `/` commands on top, since a human typing a command is a more deliberate
act than an env var inherited from a shell profile. `/tk status` always prints
the effective value *and its source*, because a silently disabled feature is
worse than a missing one.

| env var | gates | default |
|---|---|---|
| `TK_PI_SECTIONS` | master switch for all injection | 1 |
| `TK_PI_PROJECT` | the `tk_project` section | 1 |
| `TK_PI_NOTES` | the `tk_notes` TOC section | 1 |
| `TK_PI_LEDGER` | the `tk_ledger` section | 1 |
| `TK_PI_NUDGE` | the per-turn relevance probe | 1 |
| `TK_PI_SYNC` | `session_start` + `agent_end` sync | 1 |
| `TK_PI_RESIDENT` | consult, and optionally start, the resident | 1 |
| `TK_PI_EXPOSURE` | `direct` \| `deferred` \| `codemode` | `deferred` |
| `TK_PI_PROFILE` | profile handed to `registerMcpServer` | `compact` |
| `TK_MCP_PROFILE` | existing tk var; the profile default when `TK_PI_PROFILE` is unset | — |

Three rules, each from something that already bit:

* **Invalid is a hard error at load, never a silent default** — same stance as
  `resolveProfile`, where a typo'd env must not silently expose the wrong tool
  surface. `TK_PI_LEDGER=maybe` fails the session with one sentence.
* **Booleans are off-switches first.** `TK_PI_LEDGER=0` must win even when tk's
  own config enables the ledger: injection is the extension's decision, not tk's.
* **`TK_PI_EXPOSURE` must be set at registration.** Changing it at runtime means
  re-registering the server; pi's `mcp_servers_change` handler rebuilds the
  connection when `registeredConfig` changes (verified in the bundle), so the
  cost is one reconnect, and the command must say so rather than surprise.

One env invocation disables everything for a debugging session:
`TK_PI_SECTIONS=0 TK_PI_NUDGE=0 TK_PI_RESIDENT=0`.

## The `/` surface: only what env vars cannot do

Inspect, toggle at runtime, trigger on demand. Nothing else.

```
/tk status              effective settings + provenance, resident state,
                        index freshness, and measured per-tool latency
/tk sections on|off     master switch
/tk ledger on|off       tk_ledger section          (env: TK_PI_LEDGER)
/tk notes on|off        tk_notes TOC               (env: TK_PI_NOTES)
/tk nudge on|off        per-turn probe             (env: TK_PI_NUDGE)
/tk sync on|off         automatic sync             (env: TK_PI_SYNC)
/tk resident on|off|status
/tk expose <direct|deferred|codemode>               (env: TK_PI_EXPOSURE)
/tk profile <compact|scout|analysis|memory|minimal>  (env: TK_PI_PROFILE)
/tk sync                force one now (quiesce → sync → resume)
/tk recall <query>      run the probe by hand, print which gate passed or failed
/tk note <text>         capture a note now, into the review queue
```

**`/tk recall` is the one that earns its place.** It runs the nudge gates against
a real query and prints the verdict — which gate passed, which failed, and what
the score was. That turns gate tuning from guesswork into a measurement, and it
is how to answer "does this model respond to a nudge" without instrumenting a
model run first.

Deliberately not shipped, per the Unix rule in `AGENTS.md`:

* `/tk budget` — `jq` over `tk.log` already answers it, and `tk.log` records both
  faces of every call including `output.diag`;
* `/tk ledger set` — the model has `ledger_update`, and a human can read the
  file;
* `/tk find`, `/tk grep`, `/tk sync --dry-run` — those are `tk` itself. The
  extension is not a second CLI.

Every toggle echoes the new value and its source, so an override is visible in
the transcript and reversible. Two commands (`/tk expose`, `/tk profile`) cause a
server reconnect; both say "reconnecting tk…" first.

## Dead ends

* `tk <verb> <positional>` is wrong for `explain`, `trace`, `grep`, `outline`,
  `query`, `validate`, `find` — they take `--symbol` / `--pattern` / `--file` /
  `--cypher` / `--query`. And every read needs `--project`, because explicit
  routing has no default. Both failure modes return instantly, so a whole timing
  table can be nothing but argparse errors. Check the bytes, not the clock.
* `tk brief` is not a verb: the word is inside `arch`'s one-line summary,
  "Architecture brief (languages, packages, entry points, hotspots)". Not a
  missing command, and not a bug to report.
