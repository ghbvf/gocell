package grpc_test

// serve_addr_log_test.go — regression coverage for the adapter serve-path
// address logging (F8 symmetric fix). serveAddr (the Worker self-bind path) and
// serve() previously logged cfg.Addr, which is ":0" for an ephemeral port; both
// now log the resolved lis.Addr(), mirroring the bootstrap boundGRPC.boundAddr()
// funnel.

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/framework/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// TestServeAddr_LogsBoundAddr drives the Worker self-bind path with Addr ":0"
// and asserts the "server listening" log records the resolved ephemeral port,
// not ":0".
func TestServeAddr_LogsBoundAddr(t *testing.T) {
	buf := sloghelper.NewSyncBuffer()
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(buf, nil)))

	srv, err := adaptersgrpc.New(withReg(adaptersgrpc.Config{
		Addr: "127.0.0.1:0",
		TLS:  adaptersgrpc.TLSConfig{AllowInsecure: true},
	}))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Worker().Start(ctx) }()

	testwait.External(t, "grpc-server-listening-log", func() bool {
		return sloghelper.FindLogEntry(buf.String(), "server listening") != nil
	}, testtime.D2s, testtime.MediumPoll, "expected a 'server listening' log line")

	entry := sloghelper.FindLogEntry(buf.String(), "server listening")
	require.NotNil(t, entry)
	addr, _ := entry["addr"].(string)
	assert.NotEqual(t, "127.0.0.1:0", addr, "must log the resolved port, not :0")
	assert.True(t, strings.HasPrefix(addr, "127.0.0.1:"), "unexpected addr: %q", addr)

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.D5s):
		t.Fatal("Worker.Start did not return after ctx cancel")
	}
}

// TestServe_LogsResolvedAddrOnLifecycle drives the listener-injection path and
// asserts both start and drain lifecycle logs carry the resolved listener addr.
func TestServe_LogsResolvedAddrOnLifecycle(t *testing.T) {
	buf := sloghelper.NewSyncBuffer()
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(buf, nil)))

	srv, err := adaptersgrpc.New(withReg(adaptersgrpc.Config{
		Addr: "127.0.0.1:0",
		TLS:  adaptersgrpc.TLSConfig{AllowInsecure: true},
	}))
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	resolvedAddr := lis.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, lis) }()

	testwait.External(t, "grpc-server-started-serving-log", func() bool {
		return sloghelper.FindLogEntry(buf.String(), "server started serving") != nil
	}, testtime.D2s, testtime.MediumPoll, "expected a 'server started serving' log line")

	startEntry := sloghelper.FindLogEntry(buf.String(), "server started serving")
	require.NotNil(t, startEntry)
	assert.Equal(t, resolvedAddr, startEntry["addr"])

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.D5s):
		t.Fatal("Serve did not return after ctx cancel")
	}

	drainEntry := sloghelper.FindLogEntry(buf.String(), "draining")
	require.NotNil(t, drainEntry)
	assert.Equal(t, resolvedAddr, drainEntry["addr"])
}
