//go:build windows

package shutdown

import "os"

// signalsToWatch returns the OS shutdown signal set for Windows.
// Windows only delivers os.Interrupt (Ctrl-C / Ctrl-Break); SIGTERM is not
// deliverable from outside the process. Service-controller stop events
// (SERVICE_CONTROL_STOP) are out of scope here — see runtime/shutdown/doc.go.
func signalsToWatch() []os.Signal {
	return []os.Signal{os.Interrupt}
}
