# patches/cbm-android — Android (bionic) patches for codebase-memory-mcp

Applied in sort order by `scripts/vendor-cbm-android.sh` onto the tag matching
tk's `DefaultCBMPin` (`internal/config/config.go`). Regenerate against a new
pin by checking out the tag, re-applying, and re-exporting with `git diff`.

| Patch | Why (all verified on-device, Termux clang, arm64) |
|---|---|
| `0001-android-refuse-static-link.patch` | `uname -s` is `Linux` on Android, so `IS_LINUX` fires and the release habit `STATIC=1` reaches the link — where bionic has no static libc (`cannot open crtbegin_static.o`). Detects Android from `$(CC) -dM` (`__ANDROID__`, same style as `IS_MINGW`) and fails fast with the reason. |
| `0002-android-strtok-r-for-bionic-werror.patch` | The final link rule compiles all of `src/` with `-Werror`, and bionic marks `strtok` deprecated (not thread-safe) — glibc does not, so upstream never sees it. Rewrites the single local tokenization loop in `src/pipeline/pass_ensemble_routing.c` to `strtok_r` (same single-pass semantics) instead of weakening the warning gate. |
| `0003-android-no-mimalloc-override-on-bionic.patch` | The global `MI_MALLOC_OVERRIDE` + `CBM_MEM_GLOBAL_OVERRIDE` replaces malloc process-wide; on bionic the C++ runtime's exit-time flush (`std::cout` via `__emutls_get_address`) runs after mimalloc's teardown and aborts (`SIGABRT`, exit 134 on `--version`). Disabled when `IS_ANDROID`; bionic malloc stays. Verified: `--version` exits 0. |
| `0004-android-direct-runtime-open-no-root-walk.patch` | The secure-IPC endpoint walks component-wise from `/`, but Android denies `open("/", O_DIRECTORY)` (`EACCES`) to apps, so no daemon endpoint could ever be created (`secure CLI coordination could not be created (endpoint)` with an empty validation detail). `#ifdef __ANDROID__` at the top of `private_directory_tree_open` does `mkdir(path, 0700)` + `open(O_NOFOLLOW)` + `fd_set_cloexec` directly; the sandbox (OS-owned above, 0700 app-owned below) is the ancestor guarantee, and the fd-vs-path dev/ino + euid checks stay. |
| `0005-android-rename-commit-no-hardlinks.patch` | Socket publication commits `*.pending` via `link()` and verifies `st_nlink == 2`, but `/data` (f2fs) denies hard links (`EACCES`, silent SELinux `dontaudit`) while `rename`/`symlink` succeed (probed). `#ifdef __ANDROID__` commits with same-directory `renameat` (atomic) and accepts the single-name rest shape (`nlink == 1`) in verify/recovery/abort/shutdown. |
| `0006-android-mkdir-no-root-walk.patch` | Same root cause as 0004 one layer up: `mkdir_p_ex` in `src/foundation/compat_fs.c` walks from `/`, so worker-spawn scaffolding failed (`index worker could not be started`). Android fallback creates the path without the root walk. |
| `0007-android-tmpdir-env.patch` | `cbm_tmpdir()` hardcoded `/tmp`, which does not exist on Android (`TMPDIR=$PREFIX/tmp`), so `search_code` failed with `cannot create temp file`. Honors `TMPDIR`/`TEMP`/`TMP` on `__ANDROID__`. |

Deliberately NOT patched (checked, not assumed):

* `src/foundation/mem_profile.c` (`execinfo.h`, absent on bionic) — not
  compiled by `Makefile.cbm` in any target; patching dead code would only add
  rebase burden.
* `<linux/fs.h>` in `src/foundation/compat_fs.c` (`FICLONE` fast path) — the
  header ships in the NDK sysroot and the ioctl falls back to stream copy.
* `-lstdc++`, `-Wl,--wrap`, `-z noexecstack/separate-code/relro/now` — all
  link clean under Termux clang/lld (probed).
* `vendored/mimalloc` amalgamation — compiles clean (`-fsyntax-only` probed); only its process-wide override is off on Android (0003), the allocator itself still backs internal users.
* `<sys/acl.h>` uses are `__APPLE__`-guarded; `posix_spawn`, `inotify`,
  `malloc_usable_size` all exist on bionic.
