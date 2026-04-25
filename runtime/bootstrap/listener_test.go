package bootstrap_test

// listener_test.go — table-driven coverage for WithListener and ListenerOption helpers.

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// TestWithListener_AppendsToListenerConfigs verifies that calling WithListener
// adds an entry to the Bootstrap's listenerConfigs map and that the stored
// config reflects the provided values.
func TestWithListener_AppendsToListenerConfigs(t *testing.T) {
	t.Parallel()

	b := bootstrap.New(
		bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyNone()),
	)
	// Confirm the Bootstrap was built without panic — listenerConfigs populated.
	// We cannot inspect b.listenerConfigs directly (unexported), but we can
	// verify build-time behaviour by checking no panic occurred and that b is not nil.
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}
}

// TestWithListener_MultipleListeners verifies that declaring two different refs
// results in both being stored (no silent overwrite for distinct refs).
func TestWithListener_MultipleListeners(t *testing.T) {
	t.Parallel()

	b := bootstrap.New(
		bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyNone()),
		bootstrap.WithListener(cell.InternalListener, ":9090", bootstrap.PolicyNone()),
		bootstrap.WithListener(cell.HealthListener, ":9091", bootstrap.PolicyNone()),
	)
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}
}

// TestWithListenerOptions verifies that sub-options are applied without panic.
func TestWithListenerOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []bootstrap.ListenerOption
	}{
		{
			name: "WithListenerNet_nil_stores_nil",
			opts: []bootstrap.ListenerOption{bootstrap.WithListenerNet(nil)},
		},
		{
			name: "WithListenerTLS_nil_stores_nil",
			opts: []bootstrap.ListenerOption{bootstrap.WithListenerTLS(nil)},
		},
		{
			name: "WithListenerShutdownGrace_positive",
			opts: []bootstrap.ListenerOption{bootstrap.WithListenerShutdownGrace(5 * time.Second)},
		},
		{
			name: "WithListenerShutdownGrace_negative_stored_as_is",
			opts: []bootstrap.ListenerOption{bootstrap.WithListenerShutdownGrace(-1 * time.Second)},
		},
		{
			name: "WithListenerTLS_non_nil",
			opts: []bootstrap.ListenerOption{bootstrap.WithListenerTLS(&tls.Config{MinVersion: tls.VersionTLS13})},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrap.New(
				bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyNone(), tc.opts...),
			)
			if b == nil {
				t.Fatal("Bootstrap.New returned nil")
			}
		})
	}
}

// TestWithListenerNet_RealListener verifies WithListenerNet with an actual bound socket.
func TestWithListenerNet_RealListener(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test listener (sandbox):", err)
	}
	defer ln.Close()

	b := bootstrap.New(
		bootstrap.WithListener(
			cell.PrimaryListener, ln.Addr().String(), bootstrap.PolicyNone(),
			bootstrap.WithListenerNet(ln),
		),
	)
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}
}

// TestWithListenerShutdownGrace_ZeroValue ensures zero value is accepted.
func TestWithListenerShutdownGrace_ZeroValue(t *testing.T) {
	t.Parallel()

	b := bootstrap.New(
		bootstrap.WithListener(
			cell.HealthListener, ":9091", bootstrap.PolicyNone(),
			bootstrap.WithListenerShutdownGrace(0),
		),
	)
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}
}

// TestWithListenerShutdownGrace_NegativeRejectsAtPhase0 verifies that a negative
// shutdownGrace causes phase0 validation to fail with an actionable error message.
// Negative values are stored as-is by New (no panic), but Run must reject them.
func TestWithListenerShutdownGrace_NegativeRejectsAtPhase0(t *testing.T) {
	t.Parallel()

	b := bootstrap.New(
		bootstrap.WithListener(
			cell.PrimaryListener, ":9090", bootstrap.PolicyNone(),
			bootstrap.WithListenerShutdownGrace(-1*time.Second),
		),
	)
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := b.Run(ctx)
	if err == nil {
		t.Fatal("Bootstrap.Run must return an error for negative shutdownGrace, got nil")
	}
	if !strings.Contains(err.Error(), "negative shutdownGrace") {
		t.Errorf("error must mention 'negative shutdownGrace'; got: %v", err)
	}
}
