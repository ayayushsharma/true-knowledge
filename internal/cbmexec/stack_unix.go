//go:build linux || darwin

package cbmexec

import (
	"os/exec"
	"syscall"
)

// applyStackLimit beats a thin inherited soft RLIMIT_STACK before the engine
// starts. Go's os/exec cannot set child rlimits on any platform (Prlimit is
// not in this toolchain's syscall.SysProcAttr), so the raise is delegated to
// sh: raise the soft limit with the builtin, then exec the real binary in
// place. The PID, stdio, argv[0], and the argument list survive; only the
// size of the main-thread stack differs. When the soft limit already clears
// the floor, the spawn stays a direct exec, byte-identical to before.
func applyStackLimit(cmd *exec.Cmd) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_STACK, &lim); err != nil {
		return
	}
	argv, ok := stackShell(lim.Cur, lim.Max, cmd.Args[0], cmd.Args[1:])
	if !ok {
		return
	}
	cmd.Path = argv[0]
	cmd.Args = argv
}
