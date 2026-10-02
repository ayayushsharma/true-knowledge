---
title: Publish get_graph_schema in the analysis profile, on a measured argument list
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# Publish `get_graph_schema`

## The gap

`structuredCBMTools` (`internal/mcp/server.go`) listed `get_graph_schema`
alongside eleven other read tools, but no profile exposed it. `callTool` gates
on the profile first, so `hasTool` at `server.go:568` answered `-32601 unknown
tool` and the map entry was never consulted — reachable in neither the code path
it named nor the tool surface it implied membership in.

Two readings were available and they led opposite ways:

* **upstream drift** — CBM removed the tool and tk kept a stale name, so delete
  the entry
* **unwired capability** — CBM still has it and tk never published it, so add it
  to a profile

`2026-09-26-structured-cbm-payloads.md` settles it. Its *Engine contract (CBM
0.11.0, captured live)* section lists `get_graph_schema` among the thirteen read
tools accepting `format: "json"`, captured by driving `cbm cli --json`
directly. So: a real tool, unwired.

## Why it could not be wired on that evidence alone

Publishing a tool requires an `inputSchema`, and `04-MCP.md` requires every tool
to carry a truthful one. The 2026-09-26 ADR records *that* the tool accepts
`format:"json"` and gives payload shapes for eight others — it does not record
this tool's arguments. One line in the whole repository mentioned the name.

That is not enough, and guessing was the wrong move for a specific recorded
reason: the same ADR documents the `manage_adr` defect, where tk's enum
advertised `list|delete` and the engine's vocabulary was
`outline|get|sections|set_sections|update`. *"MCP advertises `manage_adr` modes
that CBM rejects."* A schema invented from plausibility is that bug, and the
2026-10-03 audit found three doc/code claims that were false for want of a
probe. So the question went to `THROWAWAY/` with the exact command, and the
answer came back from the engine.

## The measured contract (CBM 0.11.0)

Driven through `cbm cli get_graph_schema --args-file` against a real indexed
project:

| argument | required | behaviour |
|---|---|---|
| `project` | **yes** | omit it and the engine answers `missing required argument: project` |
| `limit` | no | paginates; `returned`/`total`/`has_more` move together |
| `offset` | no | skips rows; `offset:1` drops the first label |
| `format` | no | `"tree"` default, `"json"` for the payload |

```
$ cbm cli get_graph_schema --args-file <{"project":"probe"}>
node_labels: 7  (cols: label count)
  Branch 1
  Field 1
  ...
edge_types: 5  (cols: type count)
  DEFINES 5
  ...
total: 12
returned: 12
has_more: false
adr_present: false
```

Payload under `format:"json"`: `node_labels[]{label,count}`,
`edge_types[]{type,count}`, `total`, `returned`, `has_more`, `adr_present`.
An unknown project answers `project not found or not indexed` with
`available_projects`.

**One property worth recording: unknown arguments are silently ignored.**
`{"project":"probe","bogus_arg":1}` returned the full schema with no error. So a
schema that overstates the surface does not fail loudly at the engine — it
quietly does nothing, and the tool looks broken rather than wrong. That is an
argument for declaring the measured minimum and no more, which is what the
published schema does: `project` required, `limit` and `offset` optional, and
nothing else. `label`, `scopes` and `include_counts` were all tried and none
changed the output.

Also noted: `cbm cli` exits **0** on an error payload. The error is in the
JSON, not the status, so anything reading CBM's exit code for a graph tool is
reading the wrong signal. tk does not — it judges emptiness from
`structuredContent` — but the fact is recorded here because it is the kind of
thing that gets re-derived wrongly.

## The decision

**Publish it in `analysis`, beside `query_graph`, with the measured schema.**

Cypher is the reason. `query_graph` takes a read-only query and cannot be
written blind: `MATCH (n:Function)` needs to know `Function` is a label that
exists here, and `edge_types` is the list of relationships available to match.
An agent that guesses pays a round trip per wrong label, and a wrong label is a
silent empty result rather than an error.

`analysis` and not `scout`: schema discovery is a question you ask once before
writing a query, not one of the per-keystroke reads `scout` is shaped for, and
`scout`'s eleven tools are already the ones an autonomous agent reaches for
without asking. `analysis` is opt-in for deep debug, which is exactly the mode
that writes Cypher.

The map entry stays, and is now live. `analysis` is 15 tools, pinned in four
places: the `mcp` package doc, the `Profile` field comment, the
`--tool-profile` flag help, and `TestToolProfileCounts` — plus the tool name
itself in that test's `check` list, because a count that holds while the name
changes would break every agent that called the old one.

## Verified

Through `tk mcp --tool-profile analysis` against real CBM 0.11.0 and a real
indexed project:

* `tools/list` returns 15 tools including `get_graph_schema`
* `tools/call` with `{"project":"probe"}` returns the full payload in
  `structuredContent`, with the same bytes in the text block
* `{"project":"probe","limit":3}` returns 3 labels, `total:12`, `has_more:true`
* `scout` still answers `-32601 unknown tool "get_graph_schema"` and lists only
  its eleven
