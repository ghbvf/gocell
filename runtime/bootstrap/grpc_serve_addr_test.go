package bootstrap

// grpc_serve_addr_test.go — TDD coverage for gRPC serve/drain logging the
// ACTUAL bound socket address (GAP-1 follow-up F8). Before the fix the drain
// success/failure logs and the wrapped drain error all echoed cfg.addr — which
// is ":0" for an ephemeral port — instead of the real bound port. boundGRPC
// .boundAddr() funnels every bound-listener log through lis.Addr() so serve and
// drain agree, eliminating the second (config-addr) source rather than patching
// each call site.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// closeErrGRPCServer is a GRPCServer whose Close returns a fixed error, for the
// drain-failure logging path. Serve/Registrar/Probes are unused by drainAllGRPCServers.
type closeErrGRPCServer struct{ err error }

func (closeErrGRPCServer) Serve(context.Context, net.Listener) error { return nil }
func (s closeErrGRPCServer) Close(context.Context) error             { return s.err }
func (closeErrGRPCServer) Registrar() GRPCServiceRegistrar           { return &noopGRPCServiceRegistrar{} }
func (closeErrGRPCServer) Probes() []healthz.Probe                   { return nil }

// installCaptureLogger swaps the process slog default for a JSON handler over a
// concurrency-safe buffer, restoring the original on cleanup.
func installCaptureLogger(t *testing.T) *sloghelper.SyncBuffer {
	t.Helper()
	buf := sloghelper.NewSyncBuffer()
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(buf, nil)))
	return buf
}

// bindEphemeralLoopback binds 127.0.0.1:0 and returns the listener; it skips the
// test (not fails) when the sandbox forbids binding.
func bindEphemeralLoopback(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test listener (sandbox):", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	return lis
}

// TestBoundGRPC_BoundAddr verifies the accessor resolves the actual bound socket
// address (lis.Addr()), never echoing the configured ":0".
func TestBoundGRPC_BoundAddr(t *testing.T) {
	lis := bindEphemeralLoopback(t)
	bd := boundGRPC{cfg: grpcListenerConfig{addr: ":0"}, lis: lis}

	assert.Equal(t, lis.Addr().String(), bd.boundAddr())
	assert.NotEqual(t, ":0", bd.boundAddr(),
		"boundAddr must resolve the ephemeral port, not echo the configured :0")
}

// TestGRPCDrain_LogsBoundAddr asserts the drain SUCCESS log records the real
// bound port, not the configured ":0" (F8 regression).
func TestGRPCDrain_LogsBoundAddr(t *testing.T) {
	buf := installCaptureLogger(t)
	lis := bindEphemeralLoopback(t)
	boundAddr := lis.Addr().String()

	bd := boundGRPC{
		cfg:   grpcListenerConfig{ref: cell.PrimaryListener, addr: ":0", server: stubGRPCServer{}},
		lis:   lis,
		owned: true,
	}
	require.NoError(t, drainAllGRPCServers(context.Background(), []boundGRPC{bd}))

	entry := sloghelper.FindLogEntry(buf.String(), "gRPC server drained")
	require.NotNil(t, entry, "expected a drained log line")
	assert.Equal(t, boundAddr, entry["addr"],
		"drain success log must record the actual bound addr, not the configured :0")
}

// TestGRPCDrain_FailureLogsBoundAddr asserts the drain FAILURE log and the
// wrapped error both carry the bound addr, not ":0".
func TestGRPCDrain_FailureLogsBoundAddr(t *testing.T) {
	buf := installCaptureLogger(t)
	lis := bindEphemeralLoopback(t)
	boundAddr := lis.Addr().String()

	bd := boundGRPC{
		cfg:   grpcListenerConfig{ref: cell.PrimaryListener, addr: ":0", server: closeErrGRPCServer{err: errors.New("drain boom")}},
		lis:   lis,
		owned: true,
	}
	err := drainAllGRPCServers(context.Background(), []boundGRPC{bd})
	require.Error(t, err)
	assert.Contains(t, err.Error(), boundAddr,
		"wrapped drain error must carry the bound addr, not :0")

	entry := sloghelper.FindLogEntry(buf.String(), "gRPC server drain failed")
	require.NotNil(t, entry, "expected a drain-failed log line")
	assert.Equal(t, boundAddr, entry["addr"],
		"drain failure log must record the actual bound addr, not the configured :0")
}
