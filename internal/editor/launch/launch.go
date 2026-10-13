// Package launch picks and starts the best available editor backend:
// $POSTHERO_EDITOR if set, otherwise nvim, then vim, then the built-in engine.
package launch

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/builtin"
	"github.com/shinyvision/posthero/internal/editor/nvim"
	"github.com/shinyvision/posthero/internal/editor/vimpty"
	"github.com/shinyvision/posthero/internal/vim"
)

// Start starts a backend, but none of those named in skip (ones that
// couldn't start before: see editor.Exited). note explains why preferred
// backends were skipped, if any failed to start.
func Start(opts editor.Options, regs *vim.Registers, w, h int, skip ...string) (b editor.Backend, note string) {
	order := []string{"nvim", "vim", "builtin"}
	if pref := os.Getenv("POSTHERO_EDITOR"); pref != "" {
		order = append([]string{pref}, order...)
	}
	var failures []string
	for _, name := range order {
		if slices.Contains(skip, name) {
			continue
		}
		b, err := start(name, opts, regs, w, h)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if b != nil {
			return b, strings.Join(failures, "; ")
		}
	}
	// The built-in engine only fails if the file is unreadable; then it
	// starts empty.
	fallback := opts
	fallback.File = ""
	b, _ = builtin.Start(fallback, regs, w, h)
	return b, strings.Join(failures, "; ")
}

func start(name string, opts editor.Options, regs *vim.Registers, w, h int) (editor.Backend, error) {
	switch name {
	case "nvim":
		if !nvim.Available() {
			return nil, nil
		}
		return nvim.Start(opts, regs, w, h)
	case "vim":
		if !vimpty.Available() {
			return nil, nil
		}
		return vimpty.Start(opts, w, h)
	case "builtin":
		return builtin.Start(opts, regs, w, h)
	}
	return nil, fmt.Errorf("unknown editor %q in POSTHERO_EDITOR", name)
}
