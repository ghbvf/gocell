package auditcoretest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
)

// buildChainConfig holds the resolved options for BuildAuditcoreChain.
type buildChainConfig struct {
	clk    clock.Clock
	logger *slog.Logger
}

// BuildChainOption configures BuildAuditcoreChain.
type BuildChainOption func(*buildChainConfig)

// WithChainClock overrides the clock injected into the auditcore Cell and
// MemStore. Defaults to clock.Real().
func WithChainClock(c clock.Clock) BuildChainOption {
	return func(cfg *buildChainConfig) { cfg.clk = c }
}

// WithChainLogger overrides the logger injected into the auditcore Cell.
// Defaults to slog.New(slog.DiscardHandler) so test output stays clean.
func WithChainLogger(l *slog.Logger) BuildChainOption {
	return func(cfg *buildChainConfig) { cfg.logger = l }
}

// BuildAuditcoreChain wires a docker-free auditcore Cell and returns the
// bound EntryHandler for the event.session.created.v1 subscription, the
// in-memory ledger.Store (for Tail/Verify assertions), and a background
// context.
//
// Protocol construction is delegated to storetest.NewTestProtocol, the
// single allowlisted entry for ledger.NewProtocol outside cmd/* composition
// roots (AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01). The protocol shape is:
//   - HMAC-SHA256 key (storetest-supplied test key)
//   - ledger.ParseNamespaceID("auditcore")
//   - RestartRecoveryStrictTailVerify + IdempotencyContentFingerprint
//
// After proto, the wiring continues:
//   - ledger.NewMemStore(proto, clock)
//   - auditcore.NewAuditCore with all required options
//   - c.Init via cell.NewRegistryRecorder(DurabilityDemo)
//   - findSubscriptionHandler for "event.session.created.v1"
//
// The entry factory is separated into NewSessionCreatedEntry so callers
// can drive multiple different entries through one chain (e.g., to test
// multi-entry hash chains or idempotency rejection on duplicate EventID).
func BuildAuditcoreChain(t *testing.T, opts ...BuildChainOption) (
	handler outbox.EntryHandler,
	store ledger.Store,
	ctx context.Context,
) {
	t.Helper()

	cfg := &buildChainConfig{
		clk:    clock.Real(),
		logger: slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(cfg)
	}

	proto := storetest.NewTestProtocol(t)

	memStore, err := ledger.NewMemStore(proto, cfg.clk)
	if err != nil {
		t.Fatalf("auditcoretest: ledger.NewMemStore: %v", err)
	}

	c := auditcore.NewAuditCore(
		auditcore.WithClock(cfg.clk),
		auditcore.WithLedgerProtocol(proto),
		auditcore.WithLedgerStore(memStore),
		auditcore.WithEmitter(outbox.NewNoopEmitter()),
		auditcore.WithTxManager(outbox.DemoCellTxManager()),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
		auditcore.WithLogger(cfg.logger),
	)

	ctx = context.Background()
	recorder := cell.NewRegistryRecorder(map[string]any{}, outbox.DurabilityDemo)
	if err := c.Init(ctx, recorder); err != nil {
		t.Fatalf("auditcoretest: auditcore.Init: %v", err)
	}

	handler = findSubscriptionHandler(t,
		recorder.Snapshot().Subscriptions, "event.session.created.v1")

	return handler, memStore, ctx
}

// findSubscriptionHandler returns the bound EntryHandler for the named topic
// from a registry snapshot. Fails the test immediately if no subscription on
// the topic is present — that signals the auditappendsession slice's
// declarative subscription has drifted away from the expected topic.
func findSubscriptionHandler(
	t *testing.T,
	subs []cell.SubscriptionRequest,
	topic string,
) outbox.EntryHandler {
	t.Helper()
	for _, sub := range subs {
		if sub.Spec.Topic == topic {
			if sub.Handler == nil {
				t.Fatalf("subscription handler for %q must be non-nil", topic)
			}
			return sub.Handler
		}
	}
	t.Fatalf("no subscription found for topic %q (auditcore wiring drift)", topic)
	return nil // unreachable: t.Fatalf calls runtime.Goexit(); kept for compiler.
}
