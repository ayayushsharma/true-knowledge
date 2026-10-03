---
id: 2026-10-03-pi-extension-recall-in-the-harness
title: A pi extension for small local models — where the recall loop should live
status: design (proposed, not built, not measured)
---

# Where the recall loop belongs in a pi + tk session

Planning note for a pi extension that drives `tk` for a 30B-class local model
(long-horizon coding, minimal context, maximum cross-project capability). Nothing
here is built. Status values are per section.

## F1 — `tk mcp` is the slowest graph path in tk. Code-verified. **Resolved 2026-10-03.**

*True when verified, kept because it is what justified Phase 1.* `internal/mcp`
built no resident client, so every graph tool call went to `s.Run.RunJSON` /
`s.Run.RunStructured` — a fresh `cbm cli` spawn — while `Ctx.residentTry` (the
"dial first, spawn as fallback" read path) was reached only by the CLI read
verbs. Phase 1 put the same three-outcome dial inside `internal/mcp`; measured
afterwards, `tk mcp` over stdio answers `search_graph` in 16.6ms median instead of
4411ms. ADR:
`AGENT_DOCS/history/DECISIONS/2026-10-03-mcp-dials-a-running-resident-before-it-spawns.md`.

Consequence for an agent client: holding the `tk mcp` stdio process open for a
whole session buys nothing per call. Numbers from
`THROWAWAY/2026-09-29-cbm-one-shot-latency.md` (**not re-measured here — the
engine is not installed on this machine**, `tk install --check` says
`would-install pin 0.11.0`):

| path | per call |
|---|---|
| `cbm cli <tool>` one-shot | 4.9s cold, **2.0s warm** (measured, CBM 0.11.0) |
| resident socket round trip | **12–26ms** measured; the historical ~74ms was pessimistic |

So a fan-out of `find` + `explain`-shaped reads over MCP costs ~5s each, and an
empty `search_graph` costs two spawns (`callStructured` → `coverageData`) ≈10s.
Over a CLI read path the same fan-out dials the resident and costs ~74ms each.

This is the single biggest performance fact in the design, and it points the
wrong way from the obvious one: **do the harness-side fan-out over the `tk` CLI,
not over the MCP connection** — until tk grows a resident dial in `internal/mcp`.

## F2 — The MCP surface has no `find` router and no `explain` compound. Measured by reading the table.

`tools(profile)` (`internal/mcp/server.go:173-268`) lists 11 graph tools, none of
which is `tk find` or `tk explain`. The CLI has both, and `04-MCP.md` §"27B
operating rules" already states the rule the surface does not provide:

> Compound `explain` in one call instead of a find-then-fetch pair.

Over MCP that pair is `search_graph` → `trace_path` → `get_code_snippet`, three
model-visible calls for one answer. The doc's rule is unimplementable on the
surface as shipped. Two ways to close it: add the two facade tools to
`tools(profile)`, or have the extension route deterministically in TypeScript.
Closing it in tk is better — the router already exists in Go
(`internal/cli`, `find` routes regex→grep, ident→graph, NL→semantic) and a
TS re-implementation would be exactly the second router `03-COMMANDS.md` warns
about.

## F3 — `--client pi` is a deliberate stub, and `03-COMMANDS.md` does not say so.

`internal/cli/setup.go:25`:

```go
case "pi":
    return "", nil, fail("Pi integration slipped this build (no reviewed extension yet); …")
```

`--client` still *completes* `pi` (`internal/cli/admin.go:669`,
`internal/cli/setup.go:191`) and `03-COMMANDS.md:35` documents
`--client pi|opencode|claude|codex` with no caveat. So completion and docs
promise a surface the code refuses. A reviewed extension is the unblock; the
doc line and the completion entry change in the same commit.

## F4 — The long-horizon mechanism is pi's context projection, not compaction.

Verified from `dist/core/system-prompt.d.ts` and `types.d.ts`:

* `before_agent_start` exposes mutable `systemPromptOptions.sections`, and
  `diffSystemPromptSections` emits a patch only for sections that actually
  changed. Unchanged sections cost no prompt-cache invalidation.
* `context` / `context_with_system` fire **before every provider request** and
  return the message list that is actually sent. A handler can therefore both
  append a per-turn block and elide older ones.

Shape this implies: project-invariant knowledge (arch, note titles, ledger) lives
in **sections**, so it prefixes once and stays cached. The folding projection is
only worth building if per-turn injected blocks turn out to be frequent — with
the nudge design in F7 they are rare by construction, so slow transcript growth
is a non-problem and the projection is complexity bought against a hypothetical.
Keep the mechanism in mind for a future change that does inject every turn; do
not build it now.

## F5 — What pi gives us, priced

From `docs/mcp.md` and `docs/extensions.md`:

* `pi.registerMcpServer(name, config)` with `exposure` and `toolExposure` per
  tool. `exposure: "codemode"` (the default) means the server's tools are
  callable from codemode scripts and `tool_search`, and **declared to nobody** —
  zero schema tokens. `deferred` does the same plus auto-activates
  `tool_search`. `direct` declares the schema permanently.
* pi auto-activates `codemode` when a `codemode` server connects, and lists
  `codemode`/`deferred` servers in a one-line `mcp_servers` system-prompt section.
* `prepareLoadout(loadout)` can rewrite descriptions and drop declarations while
  keeping tools active and callable — the lever for trimming a schema without
  losing the tool.
* MCP results over 20 KB reach the model middle-truncated; codemode scripts get
  the whole thing. So "let the script filter" is a real option for wide queries.

The 30B-specific call: **do not make the model write QuickJS.** Script-writing is
a harder task than a JSON tool call, and pi's own reference example uses it for a
power model. For 30B, `deferred` + `tool_search` (two steps, still no JS) beats
`codemode`, and beats 22 permanent schemas.

## Proposed shape (the recommendation, in one table)

| moment | who runs it | transport | what the model sees |
|---|---|---|---|
| extension load | extension | `registerMcpServer("tk", {args:["mcp"], env:{TK_MCP_PROFILE:"memory"}, exposure:"deferred", toolExposure:{…}})` | nothing yet |
| `session_start` | extension | `tk arch`/`note toc`/`ledger get` CLI, `tk sync` if dirty | 3 sections: `tk_project`, `tk_notes`, `tk_ledger`, budgeted 2200/700/1500 |
| `before_agent_start` | model | — | sections mutate only when content changes |
| per turn | extension, tk-owned memory only | `note search`, `mem recall`, `ledger get` (in-process, no engine) | one line, only when a hit clears the bar |
| kg queries | model | the MCP tools pi already knows | nothing; the extension never calls the graph |
| `agent_end` | extension | `tk sync` + `tk detect_changes` | nothing; feeds the next turn's digest |
| ledger | extension | `ledger get`, append **only on delta** | folded anchors |
| `session_shutdown` | extension | `note_save` (lands in review queue) | draft for human approval |

## The four changes from the old architecture, and why

1. **Per-turn `kg find` + `kg explain` + `mem_recall` + `notes_search` as four
   model tool calls becomes no per-turn context load at all.** pi already knows
   the tools and calls them when it needs to. The extension's per-turn job
   narrows to a relevance probe over **tk-owned memory only**, which needs no
   spawn, no backend and no daemon (`07-MEMORY.md`) and therefore no engine
   latency. Verified live on a machine with no engine installed: `note toc` and
   `ledger get` both answered instantly while `tk arch` failed open. Graph work
   stays the model's job, on tools it already has.
2. **`ledger_update` every turn → append only when the folded value changed.**
   The ledger is five keys, append-only, folded last-write-wins; a per-turn
   write destroys the history the append-only design exists to keep and buys
   nothing, because the last write wins anyway. `ledger get` is tk-owned and
   CBM-free, so the delta check costs no engine time at all.
3. **"Start the MCP server" becomes register the server, ensure the resident.**
   The server is one `tk mcp` process for the session (pi owns its lifetime).
   With the model owning the graph calls, the resident stops being an
   extension-side convenience and becomes the model's own latency budget: until
   `internal/mcp` dials the socket, every kg tool call it makes costs ~2.0s
   warm instead of ~15ms (F1, measured), and a three-call turn pays 5.4s warm —
   10.4s cold — of pure wait. The
   extension's own reads no longer care — they are tk-owned and engine-free.
   Starting a resident is currently opt-in and never automatic
   (`03-COMMANDS.md` §The resident), so an extension that starts one needs an
   ADR, not a default.
4. **Add `detect_changes` at agent end.** The blast radius of the working-tree
   diff is the cheapest possible coherence signal for a small model: "you touched
   X, Y, Z; inbound callers are A, B" is what stops a 30B from breaking a call
   site it forgot existed, and it lands in the next turn's digest.

## Open questions, unanswered here

* Does a section unchanged between turns really leave a local provider's prefix
  cache intact end to end, or only pi's transcript? Needs a measurement against
  the actual local backend (ollama / llama.cpp), not a doc quote.
* `tk mcp` resident dial: is it a 20-line mirror of `Ctx.residentTry` inside
  `internal/mcp`, and does it break the proxy's "never launches CBM's MCP
  server" claim (it does not launch one; the resident already holds it)? ADR.
* What is the resident's behaviour when the engine is being swapped by
  `tk install cbm` mid-session? Reads are refused and fall back to a spawn
  (`03-COMMANDS.md`) — a 4.9s spike inside a turn.
* Model-side floor: is 6–7 direct schemas the right number, or does
  `prepareLoadout` + `hiddenDeclarations` let a 30B run on 3? Unmeasured.
* Extension placement: in-repo (`extensions/pi-tk/`) so the tk-side changes move
  with it, or a separate repo? In-repo couples it to the 200-line doc gate and
  `manifest.json`; separate repo couples it to nothing but the pre-release
  `--json` shapes, which carry no guarantee anyway (`00-INDEX.md`).

## F6 — A client with no extension API gets only four channels, and one of them is unused

Codex, Claude Code, Cursor and friends expose no session-start, per-turn, or
shutdown hook. What a hook-less MCP client actually has:

1. `tools/list` — schemas and descriptions, always in the system prompt.
2. `tools/call` results — the only per-turn state channel, and only after a call.
3. `initialize.instructions` — a spec field. **tk sends none**: the `initialize`
   result is `protocolVersion`, `serverInfo`, `capabilities`
   (`internal/mcp/server.go:526-532`). pi uses the first line of it in its
   `mcp_servers` prompt section; other clients vary and must be checked one by one.
4. resources / prompts — client-invoked, never automatic.

So the portable design cannot inject anything at a moment of the harness's
choosing. It has to make **the first tool call do the injection**, which means
the lever is composition and budget, not hooks. Three consequences:

* **Compound tools stop being a nicety.** `04-MCP.md` says compound `explain`
  in one call; a hook-less 30B cannot afford the three-call form at all, and
  cannot be told to prefer the cheap form by a hook. Compounds are the product.
* **The resident dial moves from P2 to P0.** A compound tool that fans out to
  five CBM calls over today's spawn-per-call server costs ~25s. It is only
  shippable once `internal/mcp` dials the resident. This inverts the earlier
  ordering: the resident is not a pi optimization, it is the precondition for
  every client.
* **Freshness needs a session-scoped check, not a hook and not a timer.**
  `source_search` already self-refreshes (`05-INDEXING.md`). The graph does not.
  The portable shape is on the server's *first* request of a session: if the
  project is dirty and the watcher is off, quiesce → `tk sync` → resume. Both
  control actions already exist (`resident.ActionQuiesce`, `ActionResume`,
  `internal/resident/resident.go:64-67`), the sync runs on the one-shot write
  path, and stdin EOF still exits instantly because nothing waits on a timer.

Routing is unchanged by any of this and must stay that way: `project` is
required on every graph tool and nothing is deduced
(`2026-10-02-project-routing-is-always-explicit.md`). A hook-less client spends
one extra round trip on `list_projects` before anything else. The cheap fix that
does not break the ADR is to make the answer better rather than the default
looser: `list_projects` gains a `cwd_project` field naming the registered project
whose path contains the server's cwd. That reports which scope matches; it does
not pick one.

## F7 — The nudge: the only per-turn injection, and its channel

If the model owns the graph calls, the extension's per-turn work is one cheap
relevance question: *is there an approved note, fact, or ledger key that this
prompt is actually about?* Everything it needs for that is tk-owned and in
process, so the probe costs two process spawns and two SQLite reads and never
touches the engine.

Channel, from `types.d.ts`: `pi.sendMessage(msg, { triggerTurn: false,
deliverAs: "nextTurn" })` appends a custom message that reaches the model without
starting a turn of its own. Fired from `before_agent_start`, the nudge is in the
same request as the user's prompt: one extra prefill of ~40 tokens, **zero extra
model calls**. It is also a real transcript entry, so the user sees what was
injected — an invisible hand on the model's shoulder is not a nudge, it is a
trick.

The `context_with_system` projection can append the same text without touching
the transcript, and is the fallback if the ordering of `nextTurn` turns out not
to precede the turn in flight. **Which one actually wins is unverified** — it is
ordering behaviour, not a documented contract, so it gets a probe extension
before anything depends on it.

Four gates, because a nudge that fires on every turn is worse than no nudge:

1. The prompt carries a concrete anchor — an identifier, a path, a quoted term,
   an error string. A vague prompt gets nothing.
2. The hit is specific, not a fuzzy BM25 top-1. Exact identifier or path match,
   or a top hit over a score floor.
3. The model did not already ask: a `tool_call` handler records the turn's tk
   tool names, and a turn that already called `note_search` or `mem_recall` is
   never nudged about notes.
4. Once per note per session, and rate-limited across turns.

The line must **name the tool**, because a 30B will not infer from prose that
`note_search` exists: `approved note "ledger anchors" may cover ledger update —
mcp__tk__note_search if you want it.`

## Not measured

Every number above is either read out of the source or cited from
`2026-09-29-cbm-one-shot-latency.md`. Nothing was re-run: this machine has no
CBM engine installed, so no end-to-end latency claim here is fresh. Re-measure
with `mise run bench-real REPO=<repo>` before any of these numbers reach an ADR.