package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/manifoldco/promptui"
)

// pickerEnabled gates the interactive project picker for humans. It must
// NEVER fire for agents or scripts: disabled under --json, non-TTY, or when
// the user turned it off (ui.picker). The picker is opt-in in the first place:
// only --select reaches it, so when this gate is closed a --select invocation
// fails naming --project rather than blocking on a pipe.
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

// fuzzyFilter is the picker's search predicate: a case-insensitive subsequence
// match over the rendered label, so "DEMO" finds "demo", "tkk" finds
// "true-knowledge", and "work/api" finds a project by its directory. Typing is
// fuzzy on purpose — the names being matched are things a human half-remembers,
// and an exact substring filter would return nothing for most of them.
//
// It is a predicate, not a ranker. promptui's Searcher signature is
// `func(input string, index int) bool`, and its filter keeps the item order it
// was given (list.search), so matches come back in registry order — which is
// sorted, so the closest match is usually at the top. Ranking per keystroke
// would mean replacing the widget with a tk-owned raw-mode loop, and tk does not
// own a terminal.
func fuzzyFilter(query, label string) bool {
	qr := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(qr) == 0 {
		return true
	}
	i := 0
	for _, r := range strings.ToLower(label) {
		if r == qr[i] {
			i++
			if i == len(qr) {
				return true
			}
		}
	}
	return false
}

// pickProject asks the human to choose among registered projects, with
// type-to-filter over names and paths. Abort (Esc/Ctrl-C) maps to the standard
// routing error so an interactive refusal and an inert failure are
// indistinguishable to callers/tracing.
//
// StartInSearchMode is what makes typing work at all: promptui only builds a
// text input when Searcher is non-nil, and with no Searcher every printable key
// is discarded and "/" is dead. Arrow keys still navigate while searching; j/k
// and h/l are query text instead (promptui gates vim keys on !searchMode).
func pickProject(c *Ctx) (string, error) {
	labels, names := pickerItems(c)
	size := len(labels)
	if size > 15 {
		size = 15
	}
	p := promptui.Select{
		Label:             "project",
		Items:             labels,
		Size:              size,
		StartInSearchMode: true,
		Searcher:          func(query string, i int) bool { return fuzzyFilter(query, labels[i]) },
	}
	sel, _, err := p.Run()
	if err != nil {
		return "", fail("pass --project (registered: %s); see `tk status`", listNames(c))
	}
	return names[sel], nil
}
