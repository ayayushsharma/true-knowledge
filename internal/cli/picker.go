package cli

import (
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
)

// pickerEnabled gates the interactive project picker for humans. It must
// NEVER fire for agents or scripts: disabled under --json, non-TTY, or when
// the user turned it off (ui.picker). When disabled, requireProject keeps its
// exact routing error, so behavior is byte-identical to pre-picker builds.
func pickerEnabled(c *Ctx) bool {
	return pickerTTY(c.G.JSON, c.Cfg.UI.Picker, isCharDevice(os.Stdin), isCharDevice(os.Stdout))
}

// pickerTTY is the injectable gate core (tests feed explicit TTY flags).
func pickerTTY(json, cfgPicker, stdinTTY, stdoutTTY bool) bool {
	if json || !cfgPicker {
		return false
	}
	return stdinTTY && stdoutTTY
}

// isCharDevice reports whether f is an interactive terminal (char device),
// the lightest TTY check available without extra dependencies.
func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// pickerItems returns the parallel label/name slices shown by pickProject.
// Label carries a path hint; the session-visible selection is the name.
func pickerItems(c *Ctx) (labels, names []string) {
	for _, n := range c.Reg.Names() {
		p := c.Reg[n]
		labels = append(labels, fmt.Sprintf("%s  (%s)", n, p.Path))
		names = append(names, n)
	}
	return labels, names
}

// pickProject asks the human to choose among registered projects. Abort
// (Esc/Ctrl-C) maps to the standard routing error so an interactive refusal
// and an inert failure are indistinguishable to callers/tracing.
func pickProject(c *Ctx) (string, error) {
	labels, names := pickerItems(c)
	size := len(labels)
	if size > 15 {
		size = 15
	}
	p := promptui.Select{
		Label: "project",
		Items: labels,
		Size:  size,
	}
	sel, _, err := p.Run()
	if err != nil {
		return "", fail("pass --project (registered: %s); see `tk status`", listNames(c))
	}
	return names[sel], nil
}
