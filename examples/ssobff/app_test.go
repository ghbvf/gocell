package main

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
)

func TestNewSSOBFFAppFailsFastWithoutServiceSecret(t *testing.T) {
	t.Setenv(ssobffServiceKeyEnv, "")

	app, err := NewSSOBFFApp(WithSSOBFFLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	require.Error(t, err)
	require.Nil(t, app)
	require.True(t, strings.Contains(err.Error(), ssobffServiceKeyEnv), "error must name missing env var: %v", err)
}

func TestNewSSOBFFApp_AcceptsInjectedListeners(t *testing.T) {
	primary := newTestListener(t)
	internal := newTestListener(t)
	health := newTestListener(t)

	app, err := NewSSOBFFApp(
		WithSSOBFFLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSSOBFFInternalServiceSecret("ssobff-test-service-secret-32b!!!"),
		WithSSOBFFListener(cell.PrimaryListener, primary),
		WithSSOBFFListener(cell.InternalListener, internal),
		WithSSOBFFListener(cell.HealthListener, health),
	)
	require.NoError(t, err)
	require.NotNil(t, app)
	require.Equal(t, primary.Addr().String(), app.PrimaryListenAddr())
	require.Equal(t, internal.Addr().String(), app.InternalListenAddr())
	require.Equal(t, health.Addr().String(), app.HealthListenAddr())
}

func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
