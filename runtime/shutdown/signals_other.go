//go:build !unix && !windows

package shutdown

import "os"

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
