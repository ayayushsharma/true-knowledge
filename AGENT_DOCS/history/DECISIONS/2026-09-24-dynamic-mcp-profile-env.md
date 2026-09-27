---
title: MCP tool profile is a runtime knob (TK_MCP_PROFILE env), not an install artifact
status: authoritative
date: 2026-09-24
supersedes: [docs/AGENT-PROFILES.md §Profiles (profile pinned via --tool-profile only)]
superseded-by: null
---

# ADR — dynamic MCP tool profile via TK_MCP_PROFILE

## Context

`tk mcp` took its tool surface from a single `--tool-profile` flag (default
scout), and `tk mcp-install` rendered client snippets with a fixed
`args: ["mcp"]` — so the profile was effectively locked at install time. Agent
clients (opencode/claude/codex) and models differ per project and per session;
a profile is a **runtime property**, not an install decision. The MCP command
itself must stay identical and the profile selection must move to where the
client already differentiates: its own `env` map.

## Decision

* `tk mcp` resolves the profile in this order:
  1. `--tool-profile` **explicitly set** (`cmd.Flags().Changed`) — explicit
     override for tests/one-offs;
  2. `TK_MCP_PROFILE` env var — the primary dynamic knob each client sets per
     model/project via its `env` map;
  3. `mcp.profile` config key (machine default, empty = unset);
  4. fallback `scout`.
* An invalid value at **any** level fails the server with the valid list
  (`scout|analysis|minimal|memory`) — never a silent fallback. A typo'd env
  must not silently expose the wrong tool surface.
* Constraint: `mcp.profile` (if set) is one of the four profiles; `config.ValidProfiles()`
  is the single source for completion and validation.
* `tk mcp-install` / `tk setup --client` gain `--tool-profile <profile>`. The
  invocation stays `args: ["mcp"]`; the profile is emitted as
  `env: {"TK_MCP_PROFILE": "<profile>"}` (scout emits no env = the default).
  Per-project/per-model differentiation then lives in each client's own env,
  not in tk — no tk-side cwd→profile detection is added.
* Config key ✓: `tk config set mcp.profile analysis|""`; `version` stays 1.

## Consequence

* Same `tk mcp` binary serves every client; nothing reinstalls when a profile
  changes — the client restarts with a new env.
* Precedence is explicit flag > env > config > scout (documented in
  AGENT-PROFILES + PATHS-CONFIG).
* e2e covers: env-driven profile, flag-beats-env, invalid env rejected,
  config fallback + unset-returns-to-scout (83 checks both modes).