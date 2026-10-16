# Affirming Linux tests from Termux: run GOOS=linux test binaries on the Android kernel

Date: 2026-10-12. Status: measured (this device, tk @ dev/2026-10-12).

## Method

Android is a Linux kernel with a userspace sandbox (bionic + seccomp). A
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c` binary is fully static:
no loader, no libc — it issues Linux syscalls straight to the host kernel.
Same arch, zero emulation overhead, seconds to run. It executes the real
GOOS=linux code paths (build-tag selection, `*_unix.go` files, XDG/socket/
process behavior) that `go build`/`go vet` only compile.

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /tmp/xplat/cli.test ./internal/cli/
./cli.test   # runs on the Android kernel as a Linux process
```

## What it proved (2026-10-12 run, all 17 test packages)

* `cli.test`: **0 failures as a Linux binary** — the 5 resident tests that
  flake on Android (`AnswersWithoutSpawning`, both refusals, lifecycle log,
  install-quiesce) pass. The flakes are Android-sandbox timing, not code
  bugs, and not Linux bugs either.
* Everything else green except two explained artifacts (below).
* Darwin compile: `go test -c` per-GOOS catches test-file portability; the
  darwin-specific surface is tiny (shared `*_unix.go` + a mapping table with
  unit tests), and the shared files ARE executed by this method.

## The two artifacts, both sandbox — not code

* `docs.test` dies `SIGSYS` in `faccessat2` via `git LookPath`. Stdlib
  proof (`internal/syscall/unix/eaccess.go`): on `GOOS=android`, `Eaccess`
  returns `ENOSYS` so callers fall back to permission bits, because app
  seccomp blocks `faccessat2`. The linux binary takes the real path and is
  killed by the same filter. On real Linux the syscall is unfiltered — the
  crash proves the method detects syscall-level deltas, not a tk bug.
* `TestSilentClientDoesNotDelayAnUnrelatedRequest` fails identically on
  both GOOS values — pre-existing timing flake, unchanged verdict.

## What this method does NOT cover

* glibc behavior (static binaries bypass libc entirely), GOARCH=x86_64
  (needs qemu-user, not on-device), real-Linux absence of seccomp, macOS
  execution (impossible on-device, period).
* If full glibc userspace is ever wanted: `proot-distro install ubuntu`
  (same-arch, no emulation) + run the suite there. Heavier; not needed
  while the static-binary method answers the GOOS question.
