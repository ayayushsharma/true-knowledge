# Evals harness stays parked for surface stability, not LLM cost

Date: 2026-10-11. Decision owner: project (verbal).

The evals harness (`history/DECISIONS/2026-09-24-evals-harness.md`) is parked
until the agent surface stabilizes — NOT because it would drag an LLM into
testing. Phase 1 as designed is LLM-free: scripted `tk` invocations, exit
codes, required substrings, probe-call ceilings from `tk.log`, `est_tokens =
chars/4` labeled proxy. CI-safe, deterministic, cheap, hardware-only
variance. The only LLM in the design is the phase-2 self-judging scorer:
deferred, never the default.

The principle to keep: test tk's contract (right tools, in budget, coverage
before absence), never model behavior (whether a 27B chooses well). The
"journeys for a 27B" are journeys a 27B *would* run, executed as scripts.

Corollary: when the harness un-parks, phase 1 stays LLM-free and phase 2
stays optional. If anyone proposes model-graded verdicts as the default,
that is a new ADR, not a reading of the existing design.
