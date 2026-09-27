# Compatible Knowledge Layer: End-to-End Implementation Specification

> STATUS: FROZEN v1 HISTORY (2026-09-22). Superseded by docs/00-AUTHORITY.md, docs/CBM-BOUNDARY.md, docs/PATHS-CONFIG.md, docs/INDEXING.md, docs/AGENT-PROFILES.md, docs/DECISIONS/2026-09-22-thin-tk-over-cbm.md. Do not implement §5.2/§6/§12/§13/§15 as written — thin tk delegates daemon/store/watcher to CBM.

## 1. Purpose and scope

This document specifies an independent, open-source-compatible implementation of a local knowledge layer for coding agents. It describes the externally observable behavior, internal wiring, data contracts, lifecycle rules, compatibility requirements, and design rationale of the existing implementation without depending on organization-specific names, repositories, credentials, infrastructure, or proprietary services.

The replacement implementation may use different languages, databases, process supervisors, or graph engines. It remains compatible when existing clients can continue to perform the same operations with the same semantics, file contracts, protocol expectations, and failure behavior.

The system provides five related capabilities:

1. **Deterministic code intelligence** — a graph of source-code structure and relationships.
2. **Repository synchronization** — registration, indexing, freshness tracking, and cross-project linking.
3. **Authoritative facts** — exact, keyed, last-write-wins values.
4. **Narrative notes** — project-scoped prose retrieved through full-text and optional local embeddings.
5. **Coding-agent integration** — context injection, model-facing tools, background synchronization, and optional external-tool discovery.

The knowledge layer is local-first. It must not require an LLM for code indexing or normal code/fact queries. A local model may be used off the query path for note embeddings and end-of-session note capture.

---

## 2. Compatibility goals

### 2.1 Required compatibility

A compatible implementation must preserve:

- The `tk`-style CLI command categories.
- The `kg_*`, `mem_*`, `note_*`, `ledger_update`, and `ops_*` model-facing tool concepts.
- Project names and project-scoped isolation.
- Global fact semantics.
- The daemon handshake and newline-delimited JSON request model, where daemon IPC is used.
- Protocol version negotiation.
- Token/character budgets and bounded outputs.
- Fail-open behavior for optional knowledge functionality.
- Single-writer protection for the shared graph store.
- Explicit note review before captured notes become searchable.
- Secret-like content protection.
- Git-HEAD-aware synchronization.
- Two-pass cross-project indexing behavior.
- Explicit daemon shutdown and cache-scoped process ownership.

### 2.2 Allowed improvements

The replacement may improve:

- Configuration validation and schema versioning.
- Database migrations.
- Cross-platform process supervision.
- Connection pooling and request cancellation.
- Graph query scheduling.
- Documentation and generated API references.
- Error codes and diagnostics, provided old clients still receive recognizable errors or compatible fallbacks.
- Storage backends, provided exported semantics remain unchanged.
- Model/profile configuration.
- Packaging and installation.

### 2.3 Non-goals

The system is not intended to:

- Replace the coding agent or local model.
- Make arbitrary source-code claims without indexed evidence.
- silently store secrets.
- inject all notes into every prompt.
- use an LLM to answer ordinary graph or fact queries.
- expose a large third-party tool catalog permanently to the model.

---

## 3. Design principles and rationale

### 3.1 Deterministic first

Code structure is answered by a parser/indexer and graph queries, not by model inference. This makes results repeatable, testable, inexpensive, and usable even when the model is unavailable.

### 3.2 Separate memory by semantic shape

The implementation deliberately uses separate stores:

| Store | Represents | Write behavior | Retrieval |
|---|---|---|---|
| Code graph | What code is structurally present | Rebuilt or incrementally updated from source | Structural graph queries |
| Facts | What exact value is authoritative now | UPSERT/overwrite | Exact key lookup |
| Notes | Why/how, decisions, runbooks | Append/update prose | Ranked search |
| Ledger | Current task state | Section replacement | Direct injection |

Combining these stores causes stale facts, poor ranking, unclear ownership, and oversized prompts. Separation allows each store to use the correct consistency and retrieval model.

### 3.3 Small model, small surface

A graph engine can expose many primitives, but small models perform better when deterministic routing and compound operations are provided. The implementation therefore keeps a complete administrative/CLI surface while exposing a smaller model-facing facade.

### 3.4 Fail open

The knowledge layer improves an agent session but must not prevent the agent from working. Startup hooks, background synchronization, context injection, embedding calls, and external integrations must degrade to no-op, warning, or reduced functionality rather than blocking the coding workflow.

### 3.5 One owner for shared mutable state

The graph engine may maintain internal files, indexes, caches, and helper processes. Multiple independent callers must not access the same graph cache concurrently without coordination. A resident daemon and a single gate provide one ownership boundary.

### 3.6 Explicit lifecycle

A resident daemon is useful because graph processes have meaningful startup cost. It must nevertheless be started, supervised, and stopped explicitly. The system must never perform broad process killing that could affect another installation or unrelated user process.

---

## 4. High-level architecture

```text
+-----------------------+       +---------------------------+
| Coding agent / CLI    |       | Optional external tools   |
|                       |       | CI, tickets, deploy APIs  |
+-----------+-----------+       +-------------+-------------+
            |                                 |
            | local CLI / MCP / extension     | on-demand gateway
            v                                 v
+----------------------------------------------------------------+
| Knowledge facade                                               |
|                                                                |
|  kg_*  code intelligence     mem_*  exact facts                |
|  note_* narrative notes      ledger task state                  |
|  ops_* progressive discovery                                      |
+-------------------------------+--------------------------------+
                                |
                                | local RPC / direct CLI
                                v
+----------------------------------------------------------------+
| Knowledge daemon                                                |
|                                                                |
|  request protocol + handshake                                  |
|  request validation                                             |
|  graph gate / scheduler                                        |
|  facts store                                                    |
|  notes store + FTS + embedding cache                           |
|  configuration and project registry                            |
|  process supervision                                            |
|  bounded output and structured logging                          |
+-------------------------------+--------------------------------+
                                |
              +-----------------+------------------+
              |                                    |
              v                                    v
+---------------------------+       +---------------------------+
| Graph/index engine        |       | Local model HTTP endpoint |
| source parsers + graph    |       | embeddings and capture    |
+---------------------------+       +---------------------------+
```

The daemon is profile-agnostic: it executes validated requests and honors budgets. Model-specific tool selection, tiering, prompt shaping, and scaffolding belong in the agent integration layer.

---

## 5. Components and code wiring

## 5.1 CLI client

The CLI is the human and script-facing entry point. It should:

1. Resolve the active home directory from `--home`, then an environment variable, then the platform default.
2. Parse commands and flags.
3. Start or locate the daemon when required.
4. Connect to the daemon.
5. Send a handshake before any operation.
6. Send one or more requests.
7. Render human-readable text by default.
8. Render stable JSON with `--json`.
9. Return non-zero exit status on operation failure.

The CLI must not implement graph semantics independently from the daemon. It should be a thin adapter so CLI, Pi, MCP, and future clients receive the same behavior.

Recommended command groups:

```text
tk init
tk register <path>
tk index [project]
tk sync
tk status
tk arch
tk find <query>
tk search <query>
tk explain <symbol>
tk grep <pattern>
tk trace <symbol>
tk impact
tk query <graph-query>
tk cbm <engine-tool>
tk mem save|recall|review
tk note save|search|toc|reindex|review
tk daemon status|stop|health
tk ui
tk serve-test
```

The low-level graph-engine passthrough is useful for administration and debugging but should not be registered as the default model-facing surface.

## 5.2 Daemon

The daemon owns:

- Local IPC.
- Handshake and protocol version checks.
- Configuration loading.
- Project registry access.
- Graph-engine process access.
- Fact and note database handles.
- Embedding cache.
- Request logging.
- Graceful shutdown.
- Graph-engine health supervision.

A daemon instance is associated with exactly one knowledge home and one graph directory. It must acquire an exclusive lock for that home before listening.

Startup sequence:

1. Create the home directory with restrictive permissions.
2. Acquire the home lock.
3. Load and validate configuration.
4. Resolve the graph engine binary.
5. Bind loopback IPC on an ephemeral port or configured endpoint.
6. Atomically write the endpoint and PID files.
7. Open facts and notes stores.
8. Initialize embedding cache.
9. Start the graph-engine supervisor.
10. Begin accepting requests.

Shutdown sequence:

1. Stop accepting new connections.
2. Wait for active requests, subject to a shutdown deadline.
3. Flush/close note and fact databases.
4. Close request logs.
5. Stop the graph-engine process belonging to this home.
6. Remove transient PID and endpoint files.
7. Release the home lock.

If a dependency fails at startup, the daemon should report the affected capability precisely. For example, a missing notes database should disable `note_*` operations without necessarily disabling graph queries.

## 5.3 Graph adapter

The graph adapter isolates the replacement implementation from the chosen graph engine. It should expose an internal interface similar to:

```text
Index(project, mode, targets) -> IndexResult
Search(project, query, options) -> Result
Trace(project, symbol, direction, mode, depth) -> Result
Architecture(project, aspects) -> Result
Impact(project, revision, direction) -> Result
SourceSearch(project, pattern, options) -> Result
RawQuery(project, query) -> Result
Health() -> HealthResult
Start() / Stop() / Restart()
```

All graph-engine invocations must pass through one spawn function. That function is responsible for:

- Environment variables.
- Graph directory selection.
- Resource limits.
- Platform-specific wrappers.
- Cancellation.
- Standard output/error capture.
- Exit-status translation.
- Structured diagnostic extraction.

This prevents a later code path from accidentally bypassing safety or platform workarounds.

## 5.4 Agent extension

The extension is responsible for Pi-specific behavior, not graph storage:

- Detecting the active project.
- Running background synchronization.
- Warming the architecture brief and notes table of contents.
- Injecting compact context.
- Registering model-facing tools.
- Selecting a tool tier based on model configuration.
- Maintaining the optional task ledger.
- Running end-of-session note capture.
- Displaying non-blocking progress.
- Suppressing all behavior when disabled.

The extension invokes the CLI or a local RPC client. It must not duplicate graph, fact, or note logic.

## 5.5 External operations gateway

External integrations are intentionally separate from the local deterministic core. The gateway should:

1. List available servers/tools compactly.
2. Return the schema for one selected operation.
3. Execute only the selected operation.
4. Preserve confirmation requirements.
5. Avoid resident processes unless the external server requires them.
6. Keep credentials and server-specific configuration outside the knowledge database.

This progressive disclosure minimizes standing context and reduces accidental external calls.

---

## 6. On-disk layout and storage contracts

The home directory is configurable and should default to a platform-appropriate user data location.

```text
<home>/
  config.json                 # validated user configuration
  config.lock                 # optional configuration write lock
  protocol.json               # optional capability metadata
  daemon.lock                 # exclusive daemon ownership
  daemon.pid                  # transient process ID
  daemon.endpoint             # transient loopback endpoint
  graph/                      # graph-engine data
  index.lock.json             # indexed Git state per project
  mem/
    facts.db                  # exact keyed facts
  notes/
    <project>/*.md            # human-readable note packs
    index.db                  # FTS and embedding metadata
    review/<project>/queue/   # captured note candidates
  review/<project>/queue/     # secret-like fact candidates
  ledger/<project>.json       # optional task state
  logs/
    daemon.log
    requests.jsonl
  cache/
    embeddings/
    external-catalog.json
```

All writes to critical metadata should use a temporary file followed by an atomic rename. File permissions should be restrictive by default because notes and facts may contain sensitive project information.

### 6.1 Configuration schema

A versioned configuration should include at least:

```json
{
  "version": 1,
  "home": "...",
  "graph": {
    "directory": "...",
    "engine": "...",
    "stack_kb": 65520,
    "ui": {
      "enabled": true,
      "host": "127.0.0.1",
      "port": 9749
    }
  },
  "repos": {
    "project-name": {
      "path": "...",
      "mode": "moderate",
      "target_projects": ["*"]
    }
  },
  "budgets": {
    "default_chars": 6000,
    "architecture_chars": 2200,
    "notes_toc_chars": 700
  },
  "embedding": {
    "enabled": true,
    "endpoint": "http://127.0.0.1:11434",
    "model": "local-embedding-model",
    "timeout_ms": 10000
  }
}
```

Improvements over an unvalidated configuration should include:

- JSON schema validation.
- Unknown-field warnings.
- Explicit defaults without silently accepting malformed values.
- A `config validate` command.
- Configuration reload semantics documented per field.
- Environment-variable overrides for testing only or clearly documented production use.
- No credentials in the configuration file.

### 6.2 Project registry

Project names must be stable identifiers. Normalize unsafe characters for filesystem use, reject empty names, and detect collisions.

Each project record contains:

- Canonical source path.
- Indexing mode.
- Cross-project targets.
- Optional display name.
- Last indexing status.
- Optional ignore/include configuration.

The source path must be canonicalized before registration. Registration should not index automatically unless explicitly requested.

### 6.3 Index lock

The index lock records the source revision used to create the current graph:

```json
{
  "project-name": {
    "path": "...",
    "mode": "moderate",
    "head": "<revision>",
    "indexed_at": 1710000000,
    "nodes": 1234,
    "edges": 5678
  }
}
```

A clean `sync` compares the current Git HEAD with this record. If unchanged, it returns quickly without invoking the graph engine.

---

## 7. Code indexing and graph semantics

### 7.1 Base indexing

Every project must first receive a normal/base index. The base pass creates source nodes and intra-project relationships.

### 7.2 Cross-project indexing

Cross-project intelligence is a linking pass, not a replacement for base indexing:

```text
for each selected project:
    run base index
    run cross-project link pass with repeated target-project flags
```

A target wildcard may represent all registered projects. The adapter must translate internal arrays into the graph engine’s required repeated-flag representation rather than passing a serialized JSON array when the engine expects repeated command-line values.

### 7.3 Indexing modes

The implementation should expose stable names such as:

- `fast` — reduced analysis for quick feedback.
- `moderate` — normal development default.
- `full` — deeper analysis.
- `cross-repo-intelligence` — base index plus linking pass.

Exact engine behavior may vary, but the names and broad performance/quality expectations should remain stable.

### 7.4 Query categories

The graph facade supports:

| Operation | Purpose |
|---|---|
| Structural search | Find symbols, packages, types, routes, and relationships |
| Literal/regex search | Find source text within indexed project files |
| Trace | Follow callers, callees, data flow, or cross-service edges |
| Architecture | Summarize languages, packages, entry points, routes, layers, and hotspots |
| Impact | Map changed symbols to affected callers |
| Raw query | Advanced graph-engine escape hatch |

The graph query path is deterministic. If semantic search is supported, it must be clearly distinguished from LLM-generated answers; embeddings may assist retrieval, but the returned evidence must still identify indexed source locations.

---

## 8. Model-facing facade

### 8.1 `kg_find`

`kg_find(query, project, mode?, limit?, budget?)` is the preferred single-entry code search.

Default routing:

- Code metacharacters, explicit regex, or source-like syntax → literal/regex search.
- Natural-language phrase → semantic or concept search.
- Short identifier/keyword → structural graph search.

The result should include:

- Selected route/mode.
- Matching locations or symbols.
- A concise recovery hint when no result is found.
- A bounded text representation.

The route must be deterministic and documented. An explicit `mode` overrides routing.

### 8.2 `kg_explain`

`kg_explain(symbol, project, budget?)` combines:

1. Symbol definition or best matching location.
2. Nearby source snippet.
3. Immediate callers.
4. Immediate callees.
5. Optional relationship labels.

This reduces a common multi-step model workflow to one bounded request.

### 8.3 `kg_grep`

`kg_grep(pattern, project, regex?, file_pattern?, limit?, budget?)` searches indexed source text. It must remain project-scoped and must distinguish invalid regex from no matches.

### 8.4 Legacy graph verbs

Older clients may continue using `kg_search`, `kg_trace`, `kg_arch`, `kg_impact`, and `kg_query`. They should remain available through the CLI and compatibility RPC layer even if newer model profiles prefer the facade verbs.

### 8.5 Output budgets

Every result path must accept or derive a character budget. Truncation should:

- Be deterministic.
- Prefer complete records over partial records.
- Include a truncation marker.
- Avoid corrupting JSON when JSON output is requested.
- Never allow an individual result to bypass the configured maximum.

---

## 9. Facts store

### 9.1 Semantic contract

Facts represent exact values that should overwrite stale values. They are not a journal and not a ranked document collection.

Logical schema:

```sql
CREATE TABLE facts (
  scope       TEXT NOT NULL,
  project     TEXT NOT NULL,
  topic       TEXT NOT NULL,
  value       TEXT NOT NULL,
  provenance  TEXT,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (scope, project, topic)
);
```

For global scope, use one documented sentinel project or a separate global-key representation. The choice is internal; the external behavior must be that a global fact is recallable from any project.

### 9.2 Operations

`mem_save`:

- Validates scope/project/topic/value.
- Detects secret-like content.
- Rejects or queues suspicious content.
- Performs an UPSERT.
- Returns whether the value was created or replaced.
- Records provenance and timestamp.

`mem_recall`:

- Retrieves by exact topic or topic substring.
- Applies project isolation for project scope.
- Searches global scope for every project.
- Returns provenance and update time.

`mem_review`:

- Lists queued writes.
- Shows a safe preview.
- Approves or rejects a candidate.
- Never silently promotes a queued secret-like value.

### 9.3 Value discipline

Facts should be short, exact, and machine-usable: endpoints, commands, identifiers, ports, feature flags, or stable operational values. Long prose, multiline explanations, and rationale belong in notes.

---

## 10. Notes store

### 10.1 Storage model

Each note is a Markdown document with metadata. A note should have a stable identifier, title, project, scope, timestamps, and body.

Example:

```markdown
---
id: note-uuid
project: project-name
scope: project
title: Rollback procedure
created_at: 1710000000
updated_at: 1710000000
pinned: false
---

The rollback must restore the previous deployment artifact before changing traffic.
```

The Markdown file is the durable human-readable source. The index database is derived state and may be rebuilt.

### 10.2 Indexing and retrieval

The notes index should provide:

- FTS5 or equivalent full-text search.
- Optional per-note embedding storage.
- Query embedding through a local model endpoint.
- Cosine similarity.
- Reciprocal-rank or documented score fusion.
- BM25-only fallback when embeddings are disabled, unavailable, or timed out.

Search must never fail solely because the embedding service is down.

### 10.3 Operations

`note_save` explicitly writes a note and updates the index.

`note_search` searches title/body within the requested scope and returns bounded excerpts, scores, and note identifiers.

`note_toc` returns title-only entries for prompt injection.

`note_reindex` rebuilds embeddings and derived indexes from Markdown.

`note_review` lists, previews, approves, and rejects captured candidates.

### 10.4 Pull-only prompt behavior

Only a small title-only table of contents may be injected automatically. Note bodies are retrieved only when the agent calls `note_search`. This preserves context budget and avoids irrelevant narrative being present on every turn.

### 10.5 Capture behavior

At session shutdown, the extension may submit a bounded transcript tail to one batched local-model call. The model proposes notes, but proposals are written to a review queue. They are not searchable until approved.

---

## 11. Task ledger

The ledger represents mutable current-task state and should not be treated as either a fact or a narrative note.

Recommended schema:

```json
{
  "goal": "...",
  "next": ["..."],
  "done": ["..."],
  "decisions": ["..."],
  "open_questions": ["..."]
}
```

`ledger_update` performs partial section replacement: sections supplied by the caller replace only those sections; omitted sections remain unchanged.

The ledger should be:

- Project-scoped.
- File-backed and easy to inspect.
- Injected transiently into the latest user context.
- Re-anchored after context compaction.
- Disable-able per session.
- Budgeted independently from notes and architecture context.

The ledger is intentionally explicit rather than automatically inferred on every turn. This reduces model calls and makes task state auditable.

---

## 12. Daemon IPC protocol

### 12.1 Transport

The compatibility transport is newline-delimited JSON over loopback TCP. A Unix-domain socket may be added as an optimization, but TCP remains the portable baseline.

The daemon writes its selected endpoint atomically to a transient endpoint file. Clients must not assume a fixed port unless configured.

### 12.2 Handshake

The first request on every connection must be a handshake:

```json
{"jsonrpc":"2.0","id":1,"method":"handshake","params":{"client_version":"1.0.0","protocol":1}}
```

Successful response:

```json
{"jsonrpc":"2.0","id":1,"result":{"server_version":"1.0.0","protocol":1}}
```

A protocol mismatch is a hard request failure:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"protocol mismatch"}}
```

Semantic versions are informational. The integer protocol version governs wire compatibility.

### 12.3 Request envelope

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "kg_find",
  "params": {
    "query": "session agent",
    "project": "project-name",
    "limit": 20,
    "budget": 6000
  }
}
```

### 12.4 Response envelope

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "ok": true,
    "text": "..."
  }
}
```

Errors use a stable numeric code and human-readable message. Clients should branch on code, not message text, while retaining the message for diagnostics.

### 12.5 Method families

A compatible daemon should support these method names or provide an explicit compatibility mapping:

```text
handshake
health
status
shutdown
sync
index
list_projects
kg_find
kg_explain
kg_grep
kg_search
kg_trace
kg_arch
kg_impact
kg_query
cbm_call
mem_save
mem_recall
mem_review_list
mem_review_resolve
note_save
note_search
note_toc
note_reindex
note_capture
note_review_list
note_review_resolve
ledger_get
ledger_update
config_reload
```

Methods requiring long-running work may emit progress events when `stream: true` is supplied. Progress events must not corrupt the final response contract.

---

## 13. Concurrency, scheduling, and process ownership

### 13.1 Graph gate

All graph-engine operations use one shared gate associated with the graph directory. The safest compatible behavior is exclusive serialization:

```text
acquire gate
run one graph operation
release gate
```

A future implementation may add read parallelism only if the graph engine explicitly guarantees concurrent safety. Indexing and reads must never run concurrently against an unsafe shared cache.

### 13.2 Supervisor

The daemon supervises one persistent graph-engine daemon where the engine supports one. The supervisor:

- Starts it during daemon startup.
- Applies required resource limits before process creation.
- Health-checks it periodically.
- Restarts it after unexpected death.
- Serializes restart with active graph operations.
- Stops only the instance belonging to the current graph directory.

### 13.3 Spawn wrapper

There must be a single graph spawn function. On platforms where the graph engine requires an increased thread stack, the wrapper must apply the limit to both normal CLI calls and persistent daemon startup. This avoids a subtle failure where short-lived calls work but long-lived index workers crash.

### 13.4 No broad cleanup

Never run a process-wide kill by executable name alone. Identify processes by:

- Current home.
- Graph directory.
- Stored PID where safe.
- Process command-line verification.

Graceful termination should be followed by a bounded force-kill fallback only for the owned process.

---

## 14. Agent session lifecycle

```text
session_start
  -> detect project
  -> start or contact daemon
  -> background sync
  -> background architecture warm
  -> background notes TOC warm

before_agent_start
  -> synchronously read already-warmed brief/TOC
  -> inject guidance + compact architecture + title-only TOC
  -> never wait for graph work on the prompt path

agent turns
  -> kg_* for deterministic code structure
  -> mem_* for exact facts
  -> note_* for narrative retrieval
  -> ledger_update for task state
  -> ops_* for external systems

agent_end / after edits
  -> schedule non-blocking sync
  -> update visible background status

session_compaction
  -> re-anchor ledger and compact guidance

session_shutdown
  -> optionally perform one batched note-capture call
  -> enqueue candidates for review
  -> do not stop a shared daemon automatically
```

The extension must suppress overlapping sync jobs. A second request while one sync is active should either coalesce with the first or return the existing job state.

---

## 15. Context injection and model profiles

### 15.1 Injected content

The system prompt should receive:

1. Compact tool-routing guidance.
2. Architecture brief.
3. Title-only notes TOC.
4. Optional task ledger.

Each item has an independent budget. The extension must not inject note bodies or the complete external tool catalog.

### 15.2 Profiles

A compatible implementation may provide nested profiles:

| Profile | Intended model | Tools | Brief | Tool result | Scaffolding |
|---|---|---:|---:|---:|---|
| minimal | Small local model | Curated search, recall, notes, ledger | Small | Small | Maximum |
| normal | General model | Facade plus common graph/memory tools | Medium | Medium | Moderate |
| full | Large/debug model | Full managed surface | Large | Large | Minimal |

The active profile can be selected explicitly or inferred from model ID/context window. Explicit configuration wins over heuristics.

Profile switching must not alter daemon protocol or storage. It only changes extension-side tool visibility and budgets.

### 15.3 Disable switches

Provide a clean extension kill switch and separate ledger switch. Disabled mode must register no tools, inject nothing, and make no daemon calls.

---

## 16. Security and privacy

### 16.1 Secret detection

Apply secret detection to:

- Fact values.
- Note titles and bodies.
- Captured transcript candidates.
- Diagnostic logs where feasible.

Suspicious writes should be redacted or queued for review, never silently persisted.

### 16.2 Local transport

Bind IPC to loopback only by default. If TCP is exposed beyond loopback, require explicit opt-in and authentication; remote access is outside the default compatibility model.

### 16.3 Filesystem permissions

Use restrictive permissions for:

- Configuration.
- Facts.
- Notes.
- Review queues.
- Logs containing request parameters.

Avoid logging full note bodies, fact values, or transcript content by default.

### 16.4 External operations

The local graph/facts/notes path must not implicitly access external systems. External calls require explicit progressive discovery and invocation.

---

## 17. Error and degradation behavior

| Condition | Required behavior |
|---|---|
| Daemon unavailable | CLI may start it; extension reports unavailable and continues |
| Graph engine unavailable | `kg_*` fails with diagnostic; facts/notes remain usable |
| Graph engine busy/conflicting | Serialize, recover owned daemon, retry once where safe |
| Embedding model unavailable | Notes search falls back to full-text |
| FTS unavailable | Notes search falls back to a documented text scan/LIKE path |
| Fact database unavailable | `mem_*` fails clearly; other tools remain usable |
| Invalid regex | Return validation error, not empty results |
| Protocol mismatch | Return explicit mismatch; do not guess wire format |
| Stale index | Report freshness; do not claim current structure |
| Secret-like write | Queue or reject; never silently store |
| Shutdown timeout | Stop accepting work, force-stop only owned processes |

Every error should include an operation, project if known, and remediation hint. Avoid dumping stack traces into model-facing output.

---

## 18. Testing specification

### 18.1 Unit tests

Cover:

- Configuration defaults and validation.
- Project-name normalization.
- Atomic metadata writes.
- Git HEAD comparison.
- Target-project argument encoding.
- Query routing.
- Output truncation.
- Fact UPSERT and global recall.
- Secret detection.
- Notes front matter parsing.
- FTS and fallback search.
- Reciprocal-rank fusion.
- Ledger partial replacement.
- Protocol parsing and mismatch handling.
- Error-code stability.

### 18.2 Integration tests

Use a temporary home directory for every test. Never write test facts, notes, graph data, or PID files into the user’s normal home.

Test:

- Daemon startup and lock exclusion.
- Handshake and multiple requests per connection.
- Concurrent clients.
- Graph gate serialization.
- Supervisor restart.
- Graceful shutdown.
- Facts and notes database lifecycle.
- CLI JSON and human output.
- Extension command invocation with fake CLI binaries.

### 18.3 Live end-to-end test

An opt-in test may use the real graph engine and a fixture repository. It should:

1. Build the current binary.
2. Create an isolated temporary home.
3. Register and index the fixture.
4. Run architecture, search, trace, impact, facts, notes, and sync queries.
5. Verify a second clean sync does no indexing.
6. Verify shutdown removes only the temporary daemon.
7. Skip clearly when the external graph binary is unavailable.

### 18.4 Compatibility tests

Maintain golden fixtures for:

- CLI command names and flags.
- RPC request/response envelopes.
- Global facts.
- Note review transitions.
- Architecture brief shape.
- Error codes.
- JSON output fields.

---

## 19. Documentation requirements

Documentation should be organized by behavior rather than implementation history:

1. **README** — purpose, install, quickstart, compatibility statement.
2. **Architecture** — components, data flow, ownership boundaries.
3. **Protocol** — complete RPC schema, handshake, errors, streaming.
4. **CLI reference** — every command, option, exit code, example.
5. **Configuration reference** — schema, defaults, overrides, migrations.
6. **Storage reference** — files, schemas, rebuild rules, permissions.
7. **Operations guide** — daemon lifecycle, indexing, troubleshooting.
8. **Agent integration guide** — hooks, tools, budgets, profiles.
9. **Security guide** — secret handling, logs, external operations.
10. **Compatibility guide** — supported versions and deprecations.
11. **Testing guide** — isolated homes, fake dependencies, live tests.

Every behavior that can surprise an operator should be documented with:

- Trigger.
- Observable result.
- Reason.
- Recovery.
- Whether data is lost or preserved.

Historical design notes should be clearly separated from current behavior so implementers do not accidentally follow superseded architecture.

---

## 20. Recommended implementation sequence

### Phase 1: Stable foundations

- Define the home/config schema.
- Implement daemon lock, endpoint, PID, and shutdown.
- Implement versioned handshake.
- Implement CLI client and JSON output.
- Add isolated test homes.

### Phase 2: Graph adapter

- Define the engine adapter interface.
- Implement the single spawn wrapper.
- Add serialized graph gate.
- Implement register, index, sync, status, and architecture/search queries.
- Add base plus cross-project indexing.

### Phase 3: Facts

- Implement SQLite schema and migrations.
- Add UPSERT, project scope, global scope, provenance, and secret review.
- Add CLI and RPC compatibility tests.

### Phase 4: Notes

- Implement Markdown persistence.
- Add FTS index and rebuild path.
- Add optional embeddings and BM25 fallback.
- Add review queue and approval workflow.

### Phase 5: Agent extension

- Add background sync and brief warming.
- Add model-facing tools.
- Add prompt guidance and title-only TOC.
- Add profiles and disable switches.
- Add ledger support.

### Phase 6: Operations and polish

- Add progressive external gateway.
- Add graph UI endpoint reporting.
- Add structured diagnostics.
- Add generated protocol/config documentation.
- Add packaging, upgrade, and migration tooling.

---

## 21. Compatibility checklist

A replacement is ready for compatibility testing when all of the following are true:

- [ ] Existing CLI commands can be mapped without changing their meaning.
- [ ] The daemon requires a first-message handshake.
- [ ] Protocol mismatch is explicit.
- [ ] `TK_HOME`/equivalent home override works for tests.
- [ ] Project registration is stable and collision-safe.
- [ ] Clean Git HEAD synchronization is a fast no-op.
- [ ] Cross-project indexing performs base indexing first.
- [ ] Graph operations are serialized against unsafe shared state.
- [ ] Every graph spawn uses the same wrapper.
- [ ] Daemon shutdown stops only owned graph processes.
- [ ] Facts overwrite by `(scope, project, topic)`.
- [ ] Global facts are visible across projects.
- [ ] Secret-like writes are queued or rejected.
- [ ] Notes remain project-scoped and searchable.
- [ ] Embedding failure falls back to full-text search.
- [ ] Captured notes require review approval.
- [ ] Prompt injection includes only bounded architecture and note titles.
- [ ] Tool results are budgeted.
- [ ] Extension hooks never block the coding prompt.
- [ ] External tools are disclosed progressively.
- [ ] Tests use isolated homes.
- [ ] Documentation describes current behavior, not only historical decisions.

---

## 22. Summary of architectural decisions

- **Go or another single-binary daemon:** reduces runtime dependency and makes CLI/agent access consistent.
- **Loopback RPC:** allows multiple clients and non-Pi integrations while remaining local.
- **Resident supervised graph process:** amortizes graph startup cost and centralizes ownership.
- **One graph gate:** prevents corruption and conflicting cache access.
- **Separate facts and notes:** preserves exact overwrite semantics for facts and ranked prose retrieval for notes.
- **Markdown as note source of truth:** keeps data inspectable, portable, and easy to migrate.
- **FTS-first notes retrieval with optional embeddings:** preserves usefulness when the local model is unavailable.
- **Deterministic facade verbs:** reduces model tool-selection burden and improves repeatability.
- **Background hooks:** keep indexing and warming off the prompt’s critical path.
- **Title-only TOC injection:** makes narrative memory discoverable without consuming the full context window.
- **Review queues:** prevent automatic persistence of secrets or low-quality model-generated memory.
- **Profile-specific budgets and tools:** adapt scaffolding to model capability without changing backend contracts.
- **Explicit compatibility layer:** permits internal replacement and improvement without breaking existing clients.
