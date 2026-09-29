//go:build linux || darwin

package resident

import "syscall"

// errAddrInUse is EADDRINUSE. The takeover decision in listen() turns on this
// one error, so it is matched exactly rather than by string.
var errAddrInUse = syscall.EADDRINUSE

// sig0 is signal 0: the "does this process exist" probe. Sending it performs
// the permission and existence checks of kill(2) without delivering anything.
var sig0 = syscall.Signal(0)
