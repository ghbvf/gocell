//go:build windows

package cmdrun

import (
	"os"
	"syscall"
)

// newSysProcAttr returns nil on Windows. Process-group kill via Job Objects
// is not yet implemented; cancellation falls back to exec.CommandContext's
// default behaviour of killing only the direct child process.
//
// TODO(B2-X-08): implement Windows process-tree cancellation using
// golang.org/x/sys/windows.AssignProcessToJobObject + a job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so that all descendants are reaped when
// the job handle is closed on ctx cancellation.
func newSysProcAttr() *syscall.SysProcAttr {
	return nil
}

// killProcessGroup falls back to killing only the direct process on Windows
// because process-group semantics differ from Unix. See TODO(B2-X-08) above.
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
