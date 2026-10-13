package vimpty

import "os"

// Windows has no SIGWINCH, and no pseudo terminals for Vim to run in.
func notifyResize(*os.Process) {}
