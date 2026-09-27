package installer

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

// output is what one backend installer run printed, split by stream.
type output struct {
	stdout string
	stderr string
}

// diagnose is the backend's own account of why it stopped, and the empty case
// when it said nothing.
//
// stderr first, structurally rather than by matching its prose: the vendor
// installer runs under set -e and both CBM and install.sh write their failures
// to stderr, while the progress lines a reader has already watched scroll past
// go to stdout. A tail of stderr is the diagnosis; a tail of stdout would be a
// replay of the install. stdout is the fallback, not the primary, because a
// refusal can be a plain message on stdout — which is exactly what "Binary is
// managed elsewhere" is.
func (o output) diagnose() []string {
	if d := diagnosticTail(o.stderr); len(d) > 0 {
		return d
	}
	return diagnosticTail(o.stdout)
}

// runScript runs the staged installer script, mirroring its output to tk's
// stderr as it arrives and keeping a copy of each stream. The copy is the
// point: the exit status is the same "exit status 1" for a full disk, a
// corrupt download, and a daemon that refused to drain.
//
// exec copies stdout and stderr on two goroutines, so the two writers share one
// mutexed sink. A plain buffer loses whichever write loses the race, which is
// how a tar error disappears from the very diagnostic meant to carry it.
func runScript(ctx context.Context, args, env []string) (output, error) {
	so, se := &syncBuffer{}, &syncBuffer{}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(os.Stderr, so)
	cmd.Stderr = io.MultiWriter(os.Stderr, se)
	err := cmd.Run()
	return output{stdout: so.String(), stderr: se.String()}, err
}

// syncBuffer is a bytes.Buffer that tolerates the two concurrent writers exec
// hands it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
