---
id: 2026-10-11-android-dev-machine-not-a-target
title: Android is a dev machine, not a target; CBM builds from a script-owned upstream tree
status: authoritative
date: 2026-10-11
supersedes: []
superseded-by: null
---

# Android is a dev machine, not a target; CBM builds from a script-owned upstream tree

`tk` builds and runs on Android (Termux, `GOOS=android`) unmodified: the module
compiled clean on-device and the XDG path layout works under `$HOME`. What does
not work there is the CBM backend: upstream publishes no Android binary, and a
glibc release could not execute on bionic anyway, so `tk install cbm` on
`android/arm64` correctly reports `cbm has no android/arm64 release`. Mapping
that pair onto the `linux-arm64` asset was considered and rejected: the binary
would download and then fail to start, which is worse than a clear error.

Deployment targets are unchanged: Linux and macOS. Android exists so an agent
can develop tk on-device, with a locally built CBM behind `TK_CBM_BIN` or
config `cbm_binary`.

## What was built

* `scripts/vendor-cbm-android.sh` (Android-only gate: refuses unless the
  compiler predefines `__ANDROID__`): clones the tag matching tk's
  `DefaultCBMPin` into `third_party/codebase-memory-mcp`, applies
  `patches/cbm-android/*.patch`, re-verifies the tree stays invisible to tk's
  own gates, and builds with CBM's `scripts/build.sh` (always dynamic).
* `patches/cbm-android/0001-android-refuse-static-link.patch`: `uname -s` is
  `Linux` on Android, so `IS_LINUX` fires and the release habit `STATIC=1`
  reaches the link, where bionic has no static libc (`cannot open
  crtbegin_static.o`, probed). Detects Android from `$(CC) -dM`
  (`__ANDROID__`, same style as `IS_MINGW`) and fails fast with the reason.
* Hiding: `/third_party/` is gitignored (also hides it from `rg`), `.cbmignore`
  carries `third_party/` so registering this repo never indexes the upstream
  tree, and CBM's nested `pkg/go/go.mod` keeps the go tool out of it for
  `./...`. The script's `--check` re-verifies per pin that no upstream
  `.md`/`.go`/`.sh`/`.json` mentions a pre-consolidation doc path and that
  upstream `.go` files are gofmt-clean, and `checkRefs` skips `third_party/`
  the way it skips `dist/`: foreign source is not tk documentation. The tree
  is deliberately NOT `./vendor/`: the go tool takes a top-level `vendor/`
  as a module vendor directory and fails every command with "inconsistent
  vendoring" unless `vendor/modules.txt` pins all of `go.mod` (probed).

## What was checked and deliberately not patched

Probed on-device (Termux clang, arm64) against CBM `v0.11.0`, not assumed:
`-lstdc++`, `-Wl,--wrap`, and the `-z noexecstack/separate-code/relro/now`
hardening flags all link clean; the mimalloc amalgamation compiles clean;
`<linux/fs.h>` ships in the NDK sysroot and the `FICLONE` fast path falls back
to stream copy; `<sys/acl.h>` uses are `__APPLE__`-guarded; `mem_profile.c`
(`execinfo.h`, absent on bionic) is not compiled by any `Makefile.cbm` target.
Patching any of those would add rebase burden for zero build effect.

## Standing follow-ups (not decisions)

* A full on-device `scripts/build.sh` run is the remaining proof; the patch
  set is minimal until a build failure says otherwise.
* `internal/cbmexec` hardcodes `/bin/sh` for the stack-floor re-exec. It exists
  on the probed device, but Termux's canonical shell is `$PREFIX/bin/sh`;
  resolving via `PATH` is future work, not this ADR.
* `runtime.GOOS == "android"` (not `"linux"`) is why the backend registry
  reports no release. If upstream ever ships an Android asset, that mapping
  gets its own ADR; until then the error stands.
