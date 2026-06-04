package grpc_test

// serve_addr_log_test.go — regression coverage for the adapter serve-path
// address logging (F8 symmetric fix). serveAddr (the Worker self-bind path) and
// serve() previously logged cfg.Addr, which is ":0" for an ephemeral port; both
// now log the resolved lis.Addr(), mirroring the bootstrap boundGRPC.boundAddr()
// funnel.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// TestServeAddr_LogsBoundAddr drives the Worker self-bind path with Addr ":0"
// and asserts the "server listening" log records the resolved ephemeral port,
// not ":0".
func TestServeAddr_LogsBoundAddr(t *testing.T) {
	buf := sloghelper.NewSyncBuffer()
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	srv, err := adaptersgrpc.New(adaptersgrpc.Config{
		Addr: "127.0.0.1:0",
		TLS:  adaptersgrpc.TLSConfig{AllowInsecure: true},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Worker().Start(ctx) }()

	require.Eventually(t, func() bool {
		return sloghelper.FindLogEntry(buf.String(), "server listening") != nil
	}, 2*time.Second, 10*time.Millisecond, "expected a 'server listening' log line")

	entry := sloghelper.FindLogEntry(buf.String(), "server listening")
	require.NotNil(t, entry)
	addr, _ := entry["addr"].(string)
	assert.NotEqual(t, "127.0.0.1:0", addr, "must log the resolved port, not :0")
	assert.True(t, strings.HasPrefix(addr, "127.0.0.1:"), "unexpected addr: %q", addr)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Worker.Start did not return after ctx cancel")
	}
}
