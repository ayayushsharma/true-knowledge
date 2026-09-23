---
title: tools/list advertises spec-compliant inputSchema (MCP 2024-11-05)
status: authoritative
date: 2026-09-24
supersedes: [docs/CBM-BOUNDARY.md §tk owns (mcp tools/list omitted inputSchema)]
superseded-by: null
---

# ADR — every MCP tool carries an inputSchema

## Context

`tk mcp`'s `tools/list` replied with only `name` + `description` per tool.
The MCP 2024-11-05 spec (the protocol version tk announces in `initialize`)
defines `Tool` as name + description + **`inputSchema`** — a JSON Schema
`{type: "object", properties, required}` — and clients use it to type-check
arguments before calling. Models that validate against the advertised schema
will refuse or mis-render tools with no schema, and downstream CLIs that
generate forms/flags from `tools/list` are blind without it. Missing
`inputSchema` is a protocol non-conformance, not a nicety.

## Decision

* Every tool advertised by `tk mcp tools/list` — all 11/14/3/20 across the
  four profiles — includes an `inputSchema` produced by `internal/mcp`
  (`toolDef.Schema` + `obj/strProp/enumProp/...` builders).
* Schemas are truthful, not decorative:
  * `required` reflects the parameters the handler/CBM actually needs
    (`project` on graph tools, `pattern`+`project` on search, `action` on
    review queues, etc.).
  * `enum` constrains closed sets: `direction` (inbound|outbound|both),
    `scope` (project|global), review `action` (list|approve|reject).
  * `manage_adr` stays an open passthrough: loose schema, no invented
    subfield contract (arguments pass through to CBM verbatim).
* Descriptions stay as the model-facing doc; parameter guidance moves into
  the schema properties.
* Wire payload for a tool:
  `{name, description, inputSchema{type:"object", properties, required?}}`.

## Consequence

* `tools/list` output grows but stays bounded — schemas are static literals,
  no per-call cost, no budget impact.
* Clients can now validate/route before `tools/call`; malformed argument
  calls still bounce with `-32602` server-side as before.
* e2e still asserts profile counts (11/14/3/20); new unit test
  `TestToolsListSchemas` checks every advertised tool carries an
  `inputSchema` of `type: object` with `properties`.