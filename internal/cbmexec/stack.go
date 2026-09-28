package cbmexec

import "strconv"

// stackFloor is the soft stack every spawned engine must run under. CBM's
// deep pipeline passes (configlink, tree-sitter walkers) recurse hard; a
// thin parent limit can reach the child as a 512KB main-thread stack on
// macOS ARM64 and overflow mid-index. 8MB is CBM's own CBM_DEFAULT_STACK_SIZE
// floor, so the wrapper enforces the same number where the spawner can.
const stackFloor = 8 << 20

// stackTarget raises a soft stack limit toward the floor and never past the
// hard limit. changed=false when the soft limit already clears the floor, so
// the spawn stays byte-identical to what the environment already grants.
func stackTarget(cur, hard uint64) (want uint64, changed bool) {
	if cur >= stackFloor {
		return cur, false
	}
	want = stackFloor
	if want > hard {
		want = hard
	}
	return want, want > cur
}

// stackShell is the macOS raising path: hand the spawn to sh, raise the soft
// limit with the builtin, then exec the real binary in place so the raised
// limit lands on the engine's main thread. $0 keeps argv[0], $@ carries the
// arguments untouched. macOS os/exec has no pre-exec rlimit hook (Prlimit is
// Linux-only), and ulimit already does the job — tk does not reimplement it.
func stackShell(soft, hard uint64, bin string, args []string) (argv []string, wrapped bool) {
	_, ok := stackTarget(soft, hard)
	if !ok {
		return nil, false
	}
	script := "ulimit -s " + stackLimitArg(hard) + " && exec \"$0\" \"$@\""
	argv = append([]string{"/bin/sh", "-c", script, bin}, args...)
	return argv, true
}

// stackLimitArg renders the hard limit for ulimit's -s operand: kilobytes,
// or the literal "unlimited" when the kernel hard limit is RLIM_INFINITY.
func stackLimitArg(hard uint64) string {
	if hard == ^uint64(0) {
		return "unlimited"
	}
	return strconv.FormatUint(hard>>10, 10)
}
