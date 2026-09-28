//go:build !linux && !darwin

package cbmexec

import "os/exec"

// applyStackLimit is a no-op where there is no stack limit to raise: Windows
// thread stacks are sized at CreateThread, and CBM sets its own size there.
func applyStackLimit(*exec.Cmd) {}
