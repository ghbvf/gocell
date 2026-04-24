//go:build !unix && !windows

package shutdown

import "os"

// signalsToWatch returns the OS shutdown signal set for platforms that are
// neither Unix nor Windows (plan9, solaris non-unix builds, js/wasm).
// Only os.Interrupt is portable.
func signalsToWatch() []os.Signal {
	return []os.Signal{os.Interrupt}
}
