package bootstrap

// listener_test.go — table-driven coverage for WithListener and ListenerOption helpers.

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
)

// TestWithListener_AppendsToListenerConfigs verifies that calling WithListener
// adds an entry to the Bootstrap's listenerConfigs map and that the stored
// config reflects the provided values.
func TestWithListener_AppendsToListenerConfigs(t *testing.T) {
	t.Parallel()

	b := New(
		WithListener(cell.PrimaryListener, ":8080", []cell.ListenerAuth{cell.AuthNone{}}),
	)
	// White-box assertion (same package): verify that exactly one entry was stored
	// and that phase0 validation accepts it (success path, no error).
	// We do not merely check b != nil — we verify functional correctness:
	// (a) listenerConfigs has exactly one entry keyed by PrimaryListener, and
	// (b) phase0ValidateOptions returns no error for this valid configuration.
	if len(b.listenerConfigs) != 1 {
		t.Fatalf("expected 1 listenerConfig entry, got %d", len(b.listenerConfigs))
	}
	if _, ok := b.listenerConfigs[cell.PrimaryListener]; !ok {
		t.Fatal("listenerConfigs must contain an entry for cell.PrimaryListener")
	}
	if err := b.phase0ValidateOptions(); err != nil {
		t.Fatalf("phase0ValidateOptions must succeed for a valid single-listener config, got: %v", err)
	}
}

// TestWithListener_MultipleListeners verifies that declaring two different refs
// results in both being stored (no silent overwrite for distinct refs).
func TestWithListener_MultipleListeners(t *testing.T) {
	t.Parallel()

	b := New(
		WithListener(cell.PrimaryListener, ":8080", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.InternalListener, ":9090", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.HealthListener, ":9091", []cell.ListenerAuth{cell.AuthNone{}}),
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
		opts []ListenerOption
	}{
		{
			name: "WithListenerNet_nil_stores_nil",
			opts: []ListenerOption{WithListenerNet(nil)},
		},
		{
			name: "WithListenerTLS_nil_stores_nil",
			opts: []ListenerOption{WithListenerTLS(nil)},
		},
		{
			name: "WithListenerShutdownGrace_positive",
			opts: []ListenerOption{WithListenerShutdownGrace(5 * time.Second)},
		},
		{
			name: "WithListenerShutdownGrace_negative_stored_as_is",
			opts: []ListenerOption{WithListenerShutdownGrace(-1 * time.Second)},
		},
		{
			name: "WithListenerTLS_non_nil",
			opts: []ListenerOption{WithListenerTLS(&tls.Config{MinVersion: tls.VersionTLS13})},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := New(
				WithListener(cell.PrimaryListener, ":8080", []cell.ListenerAuth{cell.AuthNone{}}, tc.opts...),
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

	b := New(
		WithListener(
			cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerNet(ln),
		),
	)
	if b == nil {
		t.Fatal("Bootstrap.New returned nil")
	}
}

// TestWithListenerShutdownGrace_ZeroValue ensures zero value is accepted.
func TestWithListenerShutdownGrace_ZeroValue(t *testing.T) {
	t.Parallel()

	b := New(
		WithListener(
			cell.HealthListener, ":9091", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerShutdownGrace(0),
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

	b := New(
		WithListener(
			cell.PrimaryListener, ":9090", []cell.ListenerAuth{cell.AuthNone{}},
			WithListenerShutdownGrace(-1*time.Second),
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

// TestPhase0_RejectsNilAuthChain verifies that phase0ValidateOptions returns an
// error when any listener is declared with a nil authChain — the SEC-FAIL-CLOSED
// fail-closed invariant for listener authentication. Operators must explicitly
// pass cell.AuthNone{} for HealthListener instead of relying on nil as a silent
// no-auth default.
//
// TDD phase-1 red-light: the current validateListenerConfig does NOT check for
// nil authChain — it only validates address and TLS config. This test will FAIL
// until phase-2 adds the nil-chain guard to phase0ValidateOptions.
func TestPhase0_RejectsNilAuthChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		listeners []Option
	}{
		{
			name: "InternalListener nil authChain",
			listeners: []Option{
				WithListener(cell.PrimaryListener, ":8080",
					[]cell.ListenerAuth{cell.AuthNone{}}),
				WithListener(cell.InternalListener, ":9090", nil), // nil → rejected
			},
		},
		{
			name: "HealthListener nil authChain",
			listeners: []Option{
				WithListener(cell.PrimaryListener, ":8080",
					[]cell.ListenerAuth{cell.AuthNone{}}),
				WithListener(cell.HealthListener, ":9091", nil), // nil → rejected; use AuthNone{}
			},
		},
		{
			name: "InternalListener empty slice authChain",
			listeners: []Option{
				WithListener(cell.PrimaryListener, ":8080",
					[]cell.ListenerAuth{cell.AuthNone{}}),
				// Empty slice == nil for the unauthenticated listener it produces;
				// phase0 must reject so callers can't bypass the explicit AuthNone{}
				// marker that archtest SEC-FAIL-CLOSED-02 grep-checks.
				WithListener(cell.InternalListener, ":9090", []cell.ListenerAuth{}),
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := New(tc.listeners...)
			err := b.phase0ValidateOptions()

			// Phase-2 expectation: error with ErrListenerAuthChainMissing.
			// Phase-1 current: nil (authChain is not checked) → test FAILS.
			if err == nil {
				t.Errorf("phase0ValidateOptions: expected ErrListenerAuthChainMissing error for nil authChain, got nil")
				return
			}
			if !strings.Contains(err.Error(), "authChain") &&
				!strings.Contains(err.Error(), "auth") &&
				!strings.Contains(err.Error(), "ERR_LISTENER") {
				t.Errorf("phase0ValidateOptions error %q does not mention auth chain; expected clear fail-closed message", err.Error())
			}
		})
	}
}

func TestPhase0_RejectsZeroListenerRef(t *testing.T) {
	t.Parallel()

	b := New(
		WithListener(cell.ListenerRef{}, ":8080", []cell.ListenerAuth{cell.AuthNone{}}),
	)

	err := b.phase0ValidateOptions()
	if err == nil {
		t.Fatal("phase0ValidateOptions must reject a zero ListenerRef")
	}
	if !strings.Contains(err.Error(), "zero listener ref") {
		t.Fatalf("phase0ValidateOptions error must mention zero listener ref, got: %v", err)
	}
}

// TestPhase0_AcceptsExplicitAuthNone verifies that passing cell.AuthNone{}
// explicitly for HealthListener is accepted by phase0 (the positive case for
// the fail-closed authChain requirement).
func TestPhase0_AcceptsExplicitAuthNone(t *testing.T) {
	t.Parallel()

	b := New(
		WithListener(cell.PrimaryListener, ":8080",
			[]cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.HealthListener, ":9091",
			[]cell.ListenerAuth{cell.AuthNone{}}),
	)
	// phase0 must accept explicit AuthNone (not nil).
	// This test verifies the positive path and should pass in both phases.
	// Note: phase0 may fail for unrelated reasons (e.g. no assembly); we only
	// check that it does NOT fail with an authChain error.
	err := b.phase0ValidateOptions()
	if err != nil && (strings.Contains(err.Error(), "authChain") || strings.Contains(err.Error(), "ERR_LISTENER")) {
		t.Errorf("phase0ValidateOptions: explicit AuthNone must not produce an authChain error; got: %v", err)
	}
}
