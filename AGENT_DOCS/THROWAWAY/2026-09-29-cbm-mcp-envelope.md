# The CBM MCP child returns the same payload shape as `cli --json`

**Status: measured.** CBM 0.11.0, 2026-09-29. This was the load-bearing
assumption behind the persistent-child plan and was verified before planning
on it.

## Question

If tk holds one long-lived `cbm` MCP child and sends `tools/call`, does the
reply carry the same payload tk already knows how to parse — or does it lose
`structuredContent`, which every `--json` consumer depends on?

## Answer: identical key sets

`cbm cli --json <tool>` top-level keys:

```
["content", "isError", "structuredContent"]
```

`tools/call` over the MCP child returns a reply whose `result` object has
these top-level keys:

```
["content", "structuredContent", "isError"]
```

The `content` block is a `type:"text"` entry whose `text` is the JSON of the
same object, `structuredContent` is present and populated, `isError` is
`false`. For `search_graph` the structured payload was the expected
`{qn_rule, cols, groups, total, returned, count, has_more, truncated}`.

This matches tk's own parser, which already documents the shape at
`internal/cbmexec/exec.go`:

```go
res, wrapped := raw["result"]
if !wrapped {
    // Bare MCP result object (CBM 0.11.0 `cli --json`): no wrapper.
```

## So what

The child is a **pure transport swap**. `parseEnvelope` can consume the
child's `result` verbatim; `Result{Text, Data}`, the `format:"json"`
capability memo, the legacy fallback, and every `--json` consumer are
unchanged. No reimplementation, no schema bridge.

This downgrades the persistent child from "maybe a rewrite" to "swap the
spawn for a pipe behind the existing interface".

## Caveats

Measured on one tool (`search_graph`), one engine build, one platform. The
error path was not exercised — that `isError:true` maps correctly through
`parseEnvelope` is inferred from the parser, not observed. Verify with a
forced-error call before relying on it.
