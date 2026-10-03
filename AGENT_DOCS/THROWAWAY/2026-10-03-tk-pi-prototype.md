# Prototype plan: the pi extension, buildable and falsifiable

**Question.** Is there a prototype plan good enough to build, and what does the
extension do in a directory tk has never seen?
**Status.** planned — capability matrix below is probe-verified, not inferred.
Companions: `2026-10-03-tk-pi-plan.md` (phases),
`2026-10-03-latency-budget-and-operator-surface.md` (SLO + gates + `/` surface).
**So what.** The startup check is a *capability computation*, not a boolean, and
memory does not depend on registration at all — which decides both what the
extension injects and what the nudge is allowed to do.

## Probe result: what needs a registered project, and what does not

`TK_HOME=/tmp/tk-cap`, two registered projects (`alpha`, `beta`), **CBM never
installed**, so everything below ran with the engine absent.

| capability | project arg | in an unregistered cwd |
|---|---|---|
| `note_toc` | optional = *restrict* | **all projects** — returned both `alpha note` and `beta note` |
| `note_search` | optional = *restrict* | **all projects** — searching "deploy runbook" found beta's note |
| `mem_recall` | optional = prefer this project, then all | **all projects** |
| `mem_save` | optional unless `--scope project` | **global scope works** |
| `ledger_get` / `ledger_update` | required project *name* | **works for an unregistered name** — wrote `ghost/goal`, read it back |
| `source_search` | `all_projects`, mutually exclusive with `project` | **cross-repo**, first-class |
| `search_graph`, `query_graph`, `explain`, `trace_path`, `check_index_coverage`, `index_status` | required project | unavailable *for here*; still available for any named project |
| `tk status --json` | — | engine-free: `path`, `state`, `fresh`, `zoekt_fresh`, `head`, `mode` per project |

Two consequences, both load-bearing:

1. **Memory, notes and the ledger need neither registration nor CBM.** They are
   one SQLite directory under `TK_HOME`; a project is a label in a row, not a
   foreign key. So an unregistered directory must not disable the nudge — it is
   exactly where cross-project memory is most useful.
2. **Only the graph and project-scoped text search need a project.** And
   `tk status --json` already answers the whole matrix without the engine.

## The five states the startup probe must distinguish

Not "is it registered". A human reading the section needs to know which of these
is true:

| state | code search here | graph here | memory / notes / ledger | cross-repo |
|---|---|---|---|---|
| registered, fresh | yes | yes | yes | yes |
| registered, stale | yes, after sync (off the loop) | yes, stale | yes | yes |
| registered, never indexed | no | no | yes | yes |
| **in no registered project** | no | no | **yes** | **yes** |
| no projects at all | no | no | yes | hint: `tk register .` |

Rules the prototype must obey:

* **Never invent a default project.** The graph tools get nothing in states 3–5;
  a human or the model names a project explicitly, per
  `2026-10-02-project-routing-is-always-explicit.md`.
* **Never fail closed.** State 5 renders a one-line hint and nothing else. No
  blocking, no error, no empty section.
* **State 4 and 5 are not a degraded mode.** They get the full memory surface and
  the full nudge; they lose only what genuinely needs a project.
* One line of prose per state, e.g.
  `tk: /tmp/scratch is not registered. Notes, ledger and memory work here; code
  search and the graph cover registered projects (2) — source_search with
  all_projects searches all of them.`

## One product change the prototype depends on

`list_projects` returns "node/edge counts" and **no paths**, so cwd→project
resolution over MCP is impossible and the extension would need a 30ms CLI spawn
at startup. `tk status --json` already carries `path`, `state`, `fresh`,
`zoekt_fresh`. **Add `path` and `state` to the MCP `list_projects` result**, and
the entire startup probe is one engine-free call. Parity fix, not a new feature,
and it belongs in P2.

## Layout

Repo placement is D1 and undecided; the prototype assumes
`extensions/pi-tk/` because everything except `index.ts` and `tk.ts` is a pure
function over an injected client, so it is testable with `node --test` and
portable to any MCP client later.

```
extensions/pi-tk/
  index.ts        factory: config, env gates, MCP registration, /tk commands
  capability.ts   the five-state probe        (pure, no I/O)
  sections.ts     invariant prompt sections    (pure, cacheKey per section)
  nudge.ts        the four gates              (pure)
  tk.ts           CLI ops: sync, resident autostart — the only spawns
  commands.ts     the /tk surface
  test/*.test.ts  node --test, no pi harness needed
```

## The four gates, as code

```ts
// turn text vs approved notes + facts; returns null or one line.
function nudge(turn: string, ctx: NudgeCtx): string | null
  gate1 specific   >= 2 content tokens shared with some note/fact, not stopwords
  gate2 fresh      that note was not already rendered into an earlier section
  gate3 not-done   the model made no tk note_search / mem_recall / kg call this turn
  gate4 rate       once per note per session, and >= N turns since the last nudge
  → `approved note "ledger anchors" may cover this — mcp__tk__note_search if you want it`
```

Gate 3 reads the turn's tool calls, which pi hands the extension; nothing here
needs a model turn. `/tk recall <query>` runs exactly this function and prints
which gate passed, so thresholds are tuned by measurement.

## Build order, each step ending runnable

| step | artifact | gate |
|---|---|---|
| 1 | `capability.ts` + fixtures for all five states | unit tests; probe against a live `tk status --json` |
| 2 | `sections.ts` — `tk_project`, `tk_notes`, `tk_ledger` | `node --test`; byte-identical re-render when inputs are unchanged |
| 3 | `nudge.ts` + the four gates | 20 fixtures; a turn with no relevant note yields a **zero-byte** delta |
| 4 | `index.ts` — env gates, MCP registration, sections, `before_agent_start` returning the nudge | extension loads in pi; `/tk status` shows value + provenance for all 9 gates |
| 5 | `commands.ts` — 8 commands | each toggles observable behavior; `expose`/`profile` reconnect and say so |
| 6 | `tk.ts` — `session_start` sync + resident consult | `TK_PI_*=0` disables everything and spawns nothing |

## Definition of done, all measurable

* **State 4 works.** In an unregistered directory: sections render, the nudge
  still fires, `tk_note_search` returns a cross-project hit, no crash.
* **In-loop cost holds.** After P1, a `kg_find` in the loop is under 100ms
  median; the memory probe under 3ms.
* **Quiet by default.** A turn with no relevant note produces a zero-byte delta.
* **Gates are real.** Flipping each of the 9 env vars changes observable behavior;
  `/tk status` names the source of every effective value.
* **Token cost.** Deferred exposure ≈40 always-on tokens; sections stay under a
  declared budget, and `note_toc` is already budgeted by tk.
* **Nothing engine-free gets blocked.** With CBM absent, states 4 and 5 still
  give the full memory surface.

## Dead ends

* `tk note save` takes `--project --text` and a **positional title** — not
  `--title`. `tk ledger update` takes three positionals and **no `--project`
  flag**, unlike `ledger_get` (which does accept `--project`).
* `tk mem save <topic> <value> [--project] [--scope global|project]`; the
  subcommand is `save`, not `set`. `mem_recall`'s `--project` is a preference,
  not a filter — omitting it recalls everything plus global.
* `tk brief` is not a verb — the word sits inside `arch`'s one-line summary,
  "Architecture brief (languages, packages, entry points, hotspots)". Not a
  missing command, and not a bug to report.
