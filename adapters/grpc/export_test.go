package grpc

import (
	"context"
	"net"
)

// ServeListenerForTest exposes the private serve method so integration tests
// can inject a pre-bound listener (bufconn or real loopback) without binding
// the configured Addr. This avoids port allocation races and enables TLS tests
// with well-known addresses.
func (s *Server) ServeListenerForTest(ctx context.Context, lis net.Listener) error {
	return s.serve(ctx, lis)
}
