//go:build unix

package shutdown

import (
	"os"
	"syscall"
)

// signalsToWatch returns the OS shutdown signal set for Unix-like platforms.
// SIGINT covers Ctrl-C; SIGTERM is the conventional graceful-shutdown signal
// from process supervisors (systemd, k8s).
func signalsToWatch() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
