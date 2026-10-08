# `list_projects` carries `path`, `state`, and reports the cwd

Status: **plan, not built.** Phase 2 items 3 and 7, one shape, one ADR. Written
2026-10-03.

## The issue, in one line

Over MCP a client cannot learn which registered project owns the working
directory, so the pi extension's startup probe needs a 33–56ms CLI spawn and a
guess, and `list_projects` describes the registry without the one fact that
makes it usable from a directory.

## Field parity

`tk status --json` is engine-free and already answers the whole question. The
MCP tool answers a third of it.

| field | `tk status --json` | MCP `list_projects` today | add |
|---|---|---|---|
| `project`, `mode` | ✓ | ✓ | |
| `path` | ✓ | **absent** | yes |
| `state` | ✓ | **absent** | yes |
| `fresh`, `zoekt_fresh` | ✓ | **absent** | yes |
| `head` | recorded (recorded-vs-live, per the 2026-10-03 freshness fix) | absent | yes, same meaning |
| `cwd_project` | — | — | new field, see below |

`state` and `fresh` are what let one call answer the five startup states
(registered+fresh, registered+stale, registered+never-indexed, cwd
unregistered, nothing registered) instead of the client inferring them.

## `cwd_project` reports; it never defaults

```
  reports, never            ────────────────────────────────────────────────
    "cwd_project": "tk"        project is still REQUIRED on every graph tool
    the model passes it        2026-10-02 routing ADR stands unchanged
    explicitly

  a default would be this ────────────────────────────────────────────────────
    project: "tk"  injected when omitted
    a typo stops being an error and becomes a wrong project
    a guess becomes invisible to every reader of the trace log
```

The value is the registered project whose `path` **contains** the server's cwd.
Two decisions to record, both with a real edge case:

* **Nested projects.** Longest matching `path` prefix wins, and the full list
  still comes back in `list_projects`, so the client can see that it chose the
  inner one. Shortest-prefix would report the enclosing repo of a repo.
* **No match.** `cwd_project` is absent, never `null` and never a name. An
  unregistered cwd is a real state with its own behaviour (memory, notes,
  ledger, cross-project `source_search` all still work; graph tools for "here"
  are omitted), and it must not be rendered as "the project is empty".

## Invariants this must not break

* `project` stays required on `search_graph`, `search_code`, `trace_path`,
  `query_graph`, `explain`, `check_index_coverage`, `index_status`. Adding a
  convenient field is exactly when a default sneaks in.
* `list_projects` never annotates coverage (`04-MCP.md:128`). It makes no
  "nothing exists" claim, so the fields do not change that.
* No guessing from a bare directory name. Two registered projects can both have
  a `go.mod`; the cwd is matched against `path`, full stop.

## Build order and gates

1. `path` and `state` on the existing result — mechanical, no new decision.
2. `cwd_project` — needs the nested-project rule and the absent-not-null rule
   settled first; they are the ADR.
3. Then the extension's `capability.ts` consumes this and drops its CLI spawn.

Gates, each falsifiable:

* One MCP call classifies all five startup states, with zero process spawns.
* Registered-but-stale still reports stale — the field is not recomputed from
  the live tree by the MCP face.
* `cwd_project` absent for an unregistered cwd, and no field anywhere implies a
  default project.
* Nested fixtures: a repo inside a registered repo resolves to the inner one.
* The routing ADR's own test — omit `project` from a graph tool, expect
  `-32602` — keeps passing. That is the invariant this feature is most likely
  to break, so it is the assertion that matters.