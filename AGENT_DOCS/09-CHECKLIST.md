---
id: 09-checklist
title: Operating rules and the PR gate for agents and humans
status: authoritative
date: 2026-10-02
supersedes: [AGENT_DOCS/history/DECISIONS/2026-10-02-stderr-is-the-diagnostic-channel.md, AGENT_DOCS/history/DECISIONS/2026-09-30-resident-warm-child-needs-no-freshness.md, AGENT_DOCS/history/DECISIONS/2026-09-29-measure-latency-not-daemon-routing.md, AGENTS.md, AGENT_DOCS/history/DECISIONS/2026-09-28-failed-install-is-a-failed-command.md, AGENT_DOCS/history/ROADMAP.md §Remaining-work backlog, AGENT_DOCS/history/DECISIONS/2026-09-25-comments-pass-fixes.md, AGENT_DOCS/history/DECISIONS/2026-09-27-comments-pass-2.md]
superseded-by: null
---

> Plain English on purpose. Every rule here has caused a real defect when it was
> skimmed.

# 09-CHECKLIST — how to work in this repo

## Ten rules

1. **Delegate; never reimplement.** Parsing, full-text search, embeddings, the
   watcher, the install matrix across 45 client surfaces, the UI on port 9749,
   and `.zst` artifacts all belong to CBM. If you find yourself touching a
   `*.db` directly or writing a client `mcp.json` by hand, you have a bug. Call
   `cbm cli`, or `cbm install --dry-run`.
2. **Index deliberately.** The default mode is `moderate`. Use `fast` for the
   watcher and auto-index paths, and `full` only when a person explicitly asked
   for it. Run `cross-repo-intelligence` only after fresh bases, with
   `target_projects=["*"]`, and once per source project — N sources link an
   N-repository clique. Respect `.cbmignore` ordering, the 512 MiB single-file
   cap, and `index_max_*`: an over-cap index fails whole and preserves what was
   already serving. Check `check_index_coverage` before making any negative
   claim about a codebase.
3. **Keep freshness cheap.** `tk sync` means git HEAD plus coverage. On a clean
   tree it is a no-op; otherwise let the watcher do the work. Never force a full
   index on save.
4. **Fail open on the agent, and fail loud on the operator.** If CBM is down,
   print the `tk install` hint and let the agent continue — never block it.
   `tk setup` is the fail-open face and exits `0`. `tk install` is the operator
   face: if a backend did not install, it prints the table, then exits `1`. A
   mutating command that quietly reports success is worse than one that fails.
   Truncate by whole lines, marked — never a half line. When a budget truncates output, drop whole
   records and mark it with `...truncated` or `budget_truncated`. Never
   character-slice JSON.
5. **Completion is Cobra's job, and nothing else is.** Dynamic completion comes
   from Cobra: projects, config keys, `--client`, `--tool-profile`. Test it with
   `tk __complete`. Do not hand-roll a completion engine.
6. **Close fast.** No flush and no stop on session end. stdin EOF is an instant
   exit. The one daemon tk stops is the one holding the binary it is replacing,
   and only in tk's own `CBM_RUNTIME_DIR` namespace — never an account-wide
   daemon. A report (`--check`, `--dry-run`, an up-to-date no-op) stops
   nothing. A daemon that refuses to stop is a failed install: print the
   committed pids, write nothing, and do not offer a force.
7. **Docs change the way the index does.** Write the ADR first, then the rule.
   See the change procedure in `00-INDEX.md`.
8. **A fake engine is not evidence at scale.** The shell fake in
   `tests/e2e_mvp1.sh` pins output shape and belongs in CI. It cannot see shard
   boundaries, partial coverage, result caps, or a second language. When you
   change how tk calls CBM, or when the CBM pin moves, run the real thing:

   ```bash
   mise run e2e-real REPO=/path/to/some/real/repository
   ```

   It installs the pinned CBM itself, indexes the repo, and checks nine
   properties end to end.
9. **Measure latency in layers, and never trust `tk.log` for a spawn count.**
   A perf claim without a per-layer breakdown is an opinion, and `tk.log`
   under-counts spawns (rule 8's fallback re-spawns untraced). Use
   `mise run bench` or `mise run bench-real`, and read layer F's logged-vs-
   observed columns before quoting any figure. `--verbose` narrates every spawn
   inside the wrapper, which is why it is the fastest way to see the second one.
10. **stdout is the answer; stderr is the story.** Anything a caller might pipe is
    the only thing on stdout. Progress, warnings, debug narration, and engine
    refusals go through `internal/logx` and `internal/progress`, and neither
    package may reference `os.Stdout`. A command that takes more than a second
    reports a step per stage, on every destination, including its failure path.
    Live frames are terminal-only; redirected, steps must still be one readable
    line each. `10-OUTPUT.md`.

## Before you commit

* [ ] Every test used an isolated `TK_HOME`. Nothing wrote to `~/.tk`,
      `~/Library/*`, or `%AppData%`.
* [ ] Everything that spawns CBM goes through the single `internal/cbmexec`
      wrapper, with the environment map set.
* [ ] Coverage was checked before any negative claim, budgets were honored, and
      failure paths fail open.
* [ ] Completion, `--json`, and `--help` were updated together with the
      behavior, and `tk __complete` still works.
* [ ] No secret reached `tk.log` or stderr. If a command can emit something
      secret-shaped, the redaction patterns cover it, and there is a test.
* [ ] Anything new written for a human to read went to stdout or stderr
      deliberately, not by default. A pipeable command still pipes.
* [ ] If behavior changed, there is a new dated ADR in
      `AGENT_DOCS/history/DECISIONS/` and the affected `NN-*.md` file has a
      bumped header and the new rule in its body.
* [ ] `gofmt -l .`, `go vet ./...`, and `go test ./...` are clean. If a command
      or flag changed, `mise run docs-man` produced a diff you committed.
* [ ] If this change touches how tk calls CBM, or the CBM pin moved,
      `mise run e2e-real REPO=<a real repository>` passes.
* [ ] `go test ./internal/docs` passes: no forbidden pre-consolidation path, no
      untouched history file, no file over 200 lines.

## Things that are already decided — do not relitigate

* Thin shipper over CBM, not a wrapper that grows its own engine.
* Linux-style `true-knowledge/` directories everywhere, macOS included.
* No tk supervisor daemon today; the CBM coordination daemon is shared per
  account. A `tk mcp --detach` resident is decided but unbuilt and will retire
  this line when it lands. `AGENT_DOCS/08-BACKLOG.md`.
* `daemon` is CLI-only and never exposed over MCP.
* Zoekt links as a library; CBM spawns. Zoekt is not a backend.
* Explicit commands, never magic routing. One command name never silently means
  two backends.
* Flag-only query and project forms. No positional payloads, no project
  sniffing.
* The memory layer is tk-owned, and its ledger is append-only with a human-only
  prune.
* Agents append, humans destroy.

## Where to look

| Question | File |
|---|---|
| What is tk, and what does it own | `01-ARCHITECTURE.md` |
| What tk must never do | `02-BOUNDARY.md` |
| Exact command and flag surface | `03-COMMANDS.md` |
| MCP profiles, tool lists, result shape | `04-MCP.md` |
| Index modes, discovery, freshness | `05-INDEXING.md` |
| Paths, config keys, env vars | `06-PATHS-CONFIG.md` |
| Facts, notes, ledger, secrets | `07-MEMORY.md` |
| What is not built, and why | `08-BACKLOG.md` |
| Operating rules and the PR gate | `09-CHECKLIST.md` |
| stdout vs stderr, log levels, progress | `10-OUTPUT.md` |
| Why a decision was made | `history/DECISIONS/` |
| The trace log, for real calls | `<state>/logs/tk.log` with jq |
