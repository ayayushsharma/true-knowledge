//go:build !linux && !darwin

package resident

import "os"

// errAddrInUse has no portable definition outside Unix. Windows reports a
// path conflict through the same net.OpError shape but with its own
// WSAEADDRINUSE, and the resident is not built there yet — see the Windows
// follow-up in 08-BACKLOG. Until it is, this makes the unsupported platform
// fail with a sentence instead of panicking on a nil.
var errAddrInUse = errUnsupported

// sig0 keeps the same name as the Unix probe so server.go compiles unchanged.
// On this platform the probe is a no-op: PidAlive cannot ask, so it does not
// claim, and a caller that needs certainty starts its own engine.
var sig0 os.Signal = sigZero{}

type sigZero struct{}

// Signal implements os.Signal for platforms whose Signal takes no argument.
func (sigZero) Signal() {}

// String satisfies fmt.Stringer, which os.Signal requires everywhere.
func (sigZero) String() string { return "sig0" }
