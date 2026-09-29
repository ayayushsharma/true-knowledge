//go:build linux || darwin

package resident

import (
	"os"
	"syscall"
)

// errAddrInUse is EADDRINUSE. The takeover decision in listen() turns on this
// one error, so it is matched exactly rather than by string.
var errAddrInUse = syscall.EADDRINUSE

// pidAlive asks "does this process exist" without touching it: signal 0 runs
// the existence and permission checks of kill(2) and delivers nothing.
//
// A nil return means alive OR not ours to signal. That ambiguity is safe here
// because the only caller is reporting: a pid we cannot signal is somebody
// else's, and reporting a live neighbour is the harmless answer.
func pidAlive(p *os.Process) bool {
	return p.Signal(syscall.Signal(0)) == nil
}
