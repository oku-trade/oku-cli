//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in its own session so it outlives the parent and is not
// killed by the parent's terminal signals.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
