//go:build unix

package cmdrun

import (
	"os"
	"syscall"
)

// newSysProcAttr returns a SysProcAttr that places the child process into
// its own process group. Combined with killProcessGroup, this ensures that
// ctx cancellation kills the entire subprocess tree, not just the direct
// child.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the process group rooted at p.
// The negative PID form (-pid) addresses the entire process group.
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	// syscall.Kill with a negative pid targets the entire process group.
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
