---
title: A migration that migrated nothing is a failed migration
status: authoritative
date: 2026-10-03
supersedes: null
superseded-by: null
---

# A migration that migrated nothing is a failed migration

## The gap

`03-COMMANDS.md` described `tk migrate` as one that "writes a `MIGRATED` marker,
**refuses a re-run without `--force`**". There was no `--force` flag. The command
registered `--from` and `--dry-run` and nothing else, and its own success message
admitted the gap in passing:

```
migrated 1 file(s); marker written (re-run needs --force, not yet required)
```

So the doc described a guard that did not exist, and the code advertised the
guard it was missing. What a re-run actually did:

```go
if _, err := os.Stat(dst); os.IsNotExist(err) {   // destination exists -> skip
    _ = os.WriteFile(dst, raw, 0o600)
    moved = append(moved, ...)
}
```

```
run 1:  dst missing   -> write, moved=1, stamp MIGRATED, "migrated 1 file(s)"   exit 0
run 2:  dst exists    -> skip,  moved=0, stamp MIGRATED, "migrated 0 file(s)"   exit 0
```

A user who ran it twice got a success both times and a config file from the first
run. The doc had trained them to expect a refusal, so the thing that surprised
them was the correct behaviour. That is the worst direction for a documented
guarantee to fail in: the doc is what made the silence legible as a bug.

Beside it sat `var from, dryRun string` with `_ = dryRun` — the dead `dryRun`
string from a boolean flag that had since been split into `isDry`. It cost
nothing and it documented a shape the command no longer had.

## The decision

**Register the flag the doc promised, and make a re-run that moves nothing fail.**

```go
if _, err := os.Stat(dst); err == nil && !force {
    skipped = append(skipped, f+" (exists at "+dst+")")
    continue
}
```

Three outcomes, distinguished rather than collapsed:

| situation | exit | says |
|---|---|---|
| no legacy dir found | 0 | `nothing to migrate (no legacy dirs found)` |
| legacy dir holds neither `config.json` nor `tk.json` | 0 | `nothing to migrate (no config.json or tk.json in …)` |
| every candidate already present, no `--force` | **1** | names each file, its destination, and `--force` |
| anything moved | 0 | `migrated N file(s); marker written` |

The middle row is new. The old `len(found) == 0` guard tested for *directories*,
so a legacy directory that existed but held nothing to migrate printed
`migrated 0 file(s)` and exited 0. "Found a directory" and "migrated something"
are different claims, and only one of them is supported by the evidence.

The refusal goes out through `ctx.outFailed`, not `fail`, because `--json` is
accepted by every command and a machine reading the envelope must see `ok:false`
with the diagnosis attached. `fail` alone writes nothing to stdout, which leaves
a `--json` caller with a nonzero exit and no reason for it — the same
two-faces-disagree defect as
`2026-10-03-a-refused-daemon-is-a-failed-command.md`, reached from the other
direction.

`--force` overwrites. It is deliberately not `--force-if-missing`, because the
case that needs it is a partially-migrated tree, where per-file guessing is worse
than one honest flag.

## Why not leave it idempotent

The alternative was to change the doc instead: "idempotent; a re-run migrates 0
files". That is defensible — migrations are usually idempotent by design — but it
is silent. Nothing distinguishes "already done" from "found nothing" from "found
something and every write failed", and all three exit 0 with a success line. For
a command whose whole job is moving state out of a path the user no longer
controls, the failure modes are worth separating.

Note what `--force` does **not** do: it never migrates *from* the live home. That
guard (`live[d] || live[abs]`) is unconditional and predates this change.

## The tests

`internal/cli/migrate_test.go`, all through the command:

* first run writes the config and the marker
* re-run exits 1, names `--force`, and leaves the config untouched
* re-run under `--json` renders `ok:false` with `skipped` populated
* `--force` overwrites and reports the write
* an empty legacy directory is a no-op at exit 0, not `migrated 0 file(s)`
* `--dry-run` writes neither the config nor the marker
