# Windows cannot build the module at all, so tk is unix-only for now

Date: 2026-09-30. Scoped note, not a decision.

## Finding

`GOOS=windows go build ./...` fails inside a dependency, not in tk:

```
# github.com/sourcegraph/zoekt/index
zoekt/index/builder.go:1132:27: undefined: unix.Umask
zoekt/index/builder.go:1133:7:  undefined: unix.Umask
zoekt/index/merge.go:153:20:     undefined: NewIndexFile
zoekt/index/read.go:644:16:      undefined: NewIndexFile
zoekt/index/read.go:733:12:      undefined: NewIndexFile
```

Verified pre-existing by stashing all working-tree changes and rebuilding at
`HEAD`: identical failures. `internal/zoekttext` also carries a
`syscall.Mkfifo` reference that does not exist on Windows.

## Consequence for the resident

`internal/cli/resident.go` uses `syscall.SysProcAttr{Setsid: true}`, which has
no Windows equivalent, and `internal/resident/socket_other.go` stubs the
transport. **Both are unreachable**: the module cannot compile for Windows at
all, so neither is the binding constraint.

Therefore a platform split for `Setsid` is scope creep against a documented
"Windows is a follow-up". The `socket_other.go` stub is not dead code either —
it keeps `internal/resident` itself cross-buildable, which is what lets the
transport be swapped for named pipes later without restructuring the package.

## Re-probe trigger

A `GOOS=windows go build ./...` that gets further than zoekt, or a zoekt
release that builds for Windows. Until then, treat "tk builds on Windows" as
unverified rather than false, and do not spend effort on it.

Rule 10 note: this exists because "the file uses a Unix-only syscall" reads like
a Windows blocker. It is not one. The blocker is upstream and unrelated.
