package grpc_test

import (
	"google.golang.org/grpc/test/bufconn"
)

// newBufconnListener creates an in-process bufconn listener with the given
// buffer size. Used by plaintext integration tests to avoid OS-level networking.
func newBufconnListener(bufSize int) *bufconn.Listener {
	return bufconn.Listen(bufSize)
}
