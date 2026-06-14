//go:build windows

package cmdrun

import (
	"os"
	"syscall"
)

// newSysProcAttr returns nil on Windows. Process-tree cancellation requires
// Job Objects (golang.org/x/sys/windows.AssignProcessToJobObject with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE) and a different SysProcAttr shape than
// Unix; the current Windows path is the exec.CommandContext default — direct
// child kill only — until that integration is in place.
func newSysProcAttr() *syscall.SysProcAttr {
	return nil
}

// killProcessGroup kills only the direct process on Windows. Unix process-
// group semantics (Setpgid + negative-pid signal) have no equivalent here;
// proper descendant kill needs the Job Object integration described on
// newSysProcAttr.
func killProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
