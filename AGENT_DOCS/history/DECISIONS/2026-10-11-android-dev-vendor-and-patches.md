---
id: 2026-10-11-android-dev-vendor-and-patches
title: Android CBM arrives as seven patches; build and search proven on-device
status: authoritative
date: 2026-10-11
supersedes: []
superseded-by: null
---

# Android CBM arrives as seven patches; build and search proven on-device

This closes the "remaining proof" follow-up in
`2026-10-11-android-dev-machine-not-a-target.md`: CBM `v0.11.0` (tk's
`DefaultCBMPin`) builds on Termux/arm64 with seven minimal `#ifdef
__ANDROID__` patches, and the resulting binary indexes and searches for real.
`scripts/vendor-cbm-android.sh` globs `patches/cbm-android/*.patch`, so no
script change was needed past 0001; the patch table lives in
`patches/cbm-android/README.md`.

## What each patch answers (all probed, not assumed)

* `0001` refuses `STATIC=1`: bionic has no static libc.
* `0002` `strtok` → `strtok_r`: bionic deprecates under `-Werror`.
* `0003` disables the mimalloc global override: exit-time C++ flush aborts
  after mimalloc teardown (`--version` died with SIGABRT/134, now exits 0).
* `0004` direct runtime-dir open: Android denies `open("/", O_DIRECTORY)`
  (`EACCES`), so the component-wise secure-IPC walk from `/` could never
  start. The sandbox is the ancestor guarantee; fd-level checks stay.
* `0005` `renameat` commit instead of `link()`: `/data` (f2fs) denies hard
  links while `rename`/`symlink` succeed (probed); same-dir rename is atomic.
* `0006` `mkdir_p_ex` without the root walk: same `/` denial broke worker
  scaffolding (`index worker could not be started`).
* `0007` `cbm_tmpdir()` honors `TMPDIR`: `/tmp` does not exist on Android.

## Proof

`make -f Makefile.cbm cbm` exits 0 (~302.8MB binary); `--version` exits 0;
`tk index fx` exits 0 (`[2/2] indexed fx`); `tk grep --pattern Greeter`
returns 3 through both the spawn and the resident (`tk mcp --detach`)
paths; killing the resident tears down its child and removes the socket.
Probes used along the way (`open("/")`, `link`/`rename`/`symlink` matrix,
fd-walk tracers) were throwaway by rule and are not kept.

## Procedure kept

Export patches from a clean diff of the intended change only. A 0005 export
taken with 0004 still applied carried 0004's hunk as a duplicate; the fix is
to strip the duplicate hunk so 0001→0007 apply to pristine `v0.11.0` in
order (`git apply --check` per patch, then `git apply`, before building).
Foreground the check: chaining it with a backgrounded build in one `&&`
hides `patch does not apply`.
