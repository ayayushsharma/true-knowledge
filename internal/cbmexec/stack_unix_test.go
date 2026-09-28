//go:build linux || darwin

package cbmexec

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestApplyStackLimitMatchesParentLimit checks the wrapper against the real
// process limits in the direction the environment allows: a floor-met parent
// stays a direct exec, a thin parent gets the sh raise (and never a lower
// one). The e2e suite proves the child-visible raise with a crippled parent.
func TestApplyStackLimitMatchesParentLimit(t *testing.T) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_STACK, &lim); err != nil {
		t.Skipf("getrlimit unavailable: %v", err)
	}
	cmd := exec.Command("/bin/fake-cbm", "cli", "probe")
	applyStackLimit(cmd)
	if lim.Cur >= stackFloor {
		if cmd.Path != "/bin/fake-cbm" {
			t.Fatalf("floor-met parent wrapped anyway: %q", cmd.Args)
		}
		return
	}
	if cmd.Path != "/bin/sh" {
		t.Fatalf("thin parent did not wrap: %q", cmd.Args)
	}
	wantBin := 3 // ["/bin/sh", "-c", script, bin] then args
	if len(cmd.Args) < wantBin+2 || cmd.Args[wantBin] != "/bin/fake-cbm" {
		t.Fatalf("argv corrupted: %q", cmd.Args)
	}
	if cmd.Args[2] != `ulimit -s `+stackLimitArg(lim.Max)+` && exec "$0" "$@"` {
		t.Fatalf("script does not target the hard limit: %q", cmd.Args[2])
	}
}
