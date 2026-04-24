//go:build unix

package shutdown

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
