# `instructions` is still the right field — it moved to a method that does not exist yet

Status: **plan, not built.** Phase 2 item 4. Written 2026-10-03, against the
spec as published on that date.

## The issue, in one line

A hook-less model is handed every tool schema and none of the rules for using
them, because the only always-on prose channel an MCP server has is
`instructions` — and in the newest revision that field lives on a method tk
does not implement.

## What the spec says now (verified, not recalled)

Read from the published spec, 2026-10-03. Latest revision is **2026-07-28**;
revisions in order: `2024-11-05`, `2025-03-26`, `2025-06-18`, `2025-11-25`,
`2026-07-28`.

| | `2025-06-18` (newest tk speaks) | `2025-11-25` | `2026-07-28` |
|---|---|---|---|
| handshake | `initialize` + `notifications/initialized` | same | **removed** — stateless |
| version carried | in the handshake, once | same | per request, in `_meta` |
| discovery | `initialize` | `initialize` | **`server/discover`**, `instructions` in its result |
| `resultType` on every result | — | — | **required**; clients MUST read a missing one as `complete` |
| `tools/list` cache fields | — | — | `ttlMs` + `cacheScope` required |
| tool order | unspecified | unspecified | deterministic order **SHOULD**-ed, for prompt cache hits |

So the field survived and the method moved. `server/discover` is a single
request returning `supportedVersions`, `capabilities`, `serverInfo` in `_meta`,
`instructions`, `ttlMs`, `cacheScope`. Servers MUST implement it; clients MAY
call it, and on stdio a client that supports both worlds SHOULD send it first as
a probe.

Also in 2026-07-28: `ping`, `logging/setLevel`, `notifications/roots/list_changed`
removed; multi round-trip requests replace server-initiated `roots/list`,
`sampling/createMessage`, `elicitation/create`; Roots, Sampling and Logging are
**Deprecated** and new implementations should not adopt them. tk implements none
of them, so deprecation costs nothing here.

## What pi actually speaks (from its bundle, not from docs)

```
LATEST_PROTOCOL_VERSION    = "2025-11-25"
SUPPORTED_PROTOCOL_VERSIONS = ["2025-11-25","2025-06-18","2025-03-26","2024-11-05"]
```

* pi sends `2025-11-25`; tk's ladder tops out at `2025-06-18`, so negotiation
  lands on `2025-06-18` and pi accepts it (it is in its list). No failure today.
* pi **throws** if a server selects a version outside `SUPPORTED_PROTOCOL_VERSIONS`
  — so tk must never claim a version it does not implement.
* pi reads `result.instructions` from `initialize` and keeps it. Its server
  summary is `config.description`, else the first line of `instructions`,
  capped at 250 characters.

## The decision

**Build `instructions` on `initialize` now.** It is one string in one result, it
costs ~10 tokens, it reaches every client that speaks any revision tk supports,
and pi renders it today. Nothing about the 2026-07-28 work is a prerequisite for
it.

**Do not claim 2026-07-28 support.** Serving it means a stateless read loop with
per-request `_meta` version and capabilities, a `server/discover` RPC, `resultType`
on every result, `ttlMs`/`cacheScope` on the list results, and MRTR awareness.
That is a contract change to the connection and it gets its own ADR, with a real
client asking for it as the trigger. Advertising a version tk cannot honour is
the failure mode pi's own check exists to catch.

**Three non-actions, recorded so nobody "fixes" them later:**

* Do not add `resultType`. The spec says clients MUST treat its absence as
  `complete`, so tk's results are conformant without it.
* Keep `tools/list` order deterministic. tk already returns a stable slice order;
  in 2026-07-28 this becomes a SHOULD, and for a 20–40 tok/s model a stable tool
  block is also a better prompt-cache prefix. It costs nothing and it is already
  true — write down that it is deliberate, so a future sort does not quietly
  reorder it.
* Do not add `2025-11-25` to tk's ladder yet. Everything it adds is optional
  (icons, tasks, elicitation, schema dialect) and tk implements none of it, so
  claiming the version is a claim tk has not verified against that revision. When
  it is claimed, verify it — the ladder is the place where an unverified claim
  becomes a client-side exception.

## Content: three lines, or none

The channel is cheap and the temptation is to fill it. Candidate rules, each
already stated somewhere in `AGENT_DOCS/`:

1. `project` is always explicit — no inferred default.
2. Check coverage before any negative claim; empty results already carry a verdict.
3. Compound `explain` instead of a find-then-fetch pair.

Three lines, ~40 tokens, against 1358 tokens of `scout` schemas. Gate: the first
line renders in pi's `mcp_servers` section within the 250-character cap, and
`/tk status` reports it. If the prose has to be truncated to fit the cap, the
prose is too long — cut it rather than wrapping it.