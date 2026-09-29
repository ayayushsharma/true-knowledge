//go:build !linux && !darwin

package resident

import "os"

// errAddrInUse has no portable definition outside Unix. Windows reports a
// path conflict through the same net.OpError shape but with its own
// WSAEADDRINUSE, and the resident is not built there yet — see the Windows
// follow-up in 08-BACKLOG. Until it is, this makes the unsupported platform
// fail with a sentence instead of panicking on a nil.
var errAddrInUse = errUnsupported

// pidAlive reports false because liveness is unknowable here, not because the
// process is gone.
//
// An earlier version of this file defined a no-op os.Signal whose Signal()
// returned nil, which made this answer TRUE for every syntactically valid pid
// — a stale pid file would have been reported as a live resident forever, and
// the comment claimed the opposite. A reporting helper that cannot ask must say
// "unknown", and the only honest encoding of unknown in a bool is false: the
// caller then reports no resident, which is a gap someone can act on, rather
// than a warm resident that does not exist.
//
// It is not used to decide socket takeover, so the conservative direction costs
// nothing. If that ever changes, this needs a real probe, not a default.
func pidAlive(*os.Process) bool { return false }
