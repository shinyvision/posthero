//go:build !windows

package vimpty

import (
	"os"
	"syscall"
)

func notifyResize(p *os.Process) { _ = p.Signal(syscall.SIGWINCH) }
