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
)

// BuildChainOption overrides a component in the default BuildAuditcoreChain
// assembly. All options are functional and compose cleanly; unknown defaults
// remain in effect for any option not provided.
type BuildChainOption func(*buildChainConfig)

type buildChainConfig struct {
	hmacKey []byte
	store   ledger.Store
	emitter outbox.Emitter
	clk     clock.Clock
}

// WithHMACKey overrides the HMAC key used by ledger.Protocol.
// The key must be at least 32 bytes; shorter keys are rejected by
// ledger.NewProtocol and will cause the test to fail.
func WithHMACKey(key []byte) BuildChainOption {
	return func(c *buildChainConfig) { c.hmacKey = key }
}

// WithStore overrides the ledger.Store. The provided store is used as-is;
// callers are responsible for its lifecycle.
func WithStore(s ledger.Store) BuildChainOption {
	return func(c *buildChainConfig) { c.store = s }
}

// WithEmitter overrides the outbox.Emitter injected into AuditCore.
// Use WithEmitter(outboxtest.NewRecorder()) to capture emitted entries.
func WithEmitter(e outbox.Emitter) BuildChainOption {
	return func(c *buildChainConfig) { c.emitter = e }
}

// WithClock overrides the clock.Clock injected into AuditCore and MemStore.
func WithClock(clk clock.Clock) BuildChainOption {
	return func(c *buildChainConfig) { c.clk = clk }
}

// defaultHMACKey is the 32-byte HMAC key used when no WithHMACKey option is
// provided. The value is chosen to be human-readable and sufficiently long.
var defaultHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

// BuildAuditcoreChain constructs a docker-free auditcore Cell + MemStore
// Ledger and returns the auditappendsession subscription handler.
//
// Default assembly:
//   - HMAC key: defaultHMACKey (32 bytes)
//   - Store: ledger.NewMemStore backed by the protocol above
//   - Emitter: outbox.NewNoopEmitter()
//   - TxManager: cell.DemoCellTxManager()
//   - Logger: slog.DiscardHandler (suppresses demo-mode lifecycle warnings)
//   - Clock: clock.Real()
//
// The returned handler is the bound outbox.EntryHandler for
// event.session.created.v1 registered by auditappendsession. The store is the
// same MemStore used by the cell, so callers can probe Tail/Verify after
// invoking the handler.
//
// t.Cleanup is registered; callers do not need to tear down anything manually.
func BuildAuditcoreChain(t *testing.T, opts ...BuildChainOption) (
	handler outbox.EntryHandler,
	store ledger.Store,
	ctx context.Context,
) {
	t.Helper()

	cfg := &buildChainConfig{
		hmacKey: defaultHMACKey,
		clk:     clock.Real(),
		emitter: outbox.NewNoopEmitter(),
	}
	for _, o := range opts {
		o(cfg)
	}

	ns, err := ledger.ParseNamespaceID("auditcore")
	fatalOnErr(t, err, "namespace parse")

	proto, err := ledger.NewProtocol(
		ledger.WithChainHMAC(cfg.hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	fatalOnErr(t, err, "ledger.NewProtocol")

	if cfg.store == nil {
		memStore, err := ledger.NewMemStore(proto, cfg.clk)
		fatalOnErr(t, err, "ledger.NewMemStore")
		cfg.store = memStore
	}

	c := auditcore.NewAuditCore(
		auditcore.WithClock(cfg.clk),
		auditcore.WithLedgerProtocol(proto),
		auditcore.WithLedgerStore(cfg.store),
		auditcore.WithEmitter(cfg.emitter),
		auditcore.WithTxManager(cell.DemoCellTxManager()),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
		auditcore.WithLogger(slog.New(slog.DiscardHandler)),
	)

	ctx = context.Background()
	recorder := cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)
	fatalOnErr(t, c.Init(ctx, recorder), "auditcore.Init")

	handler = findSubscriptionHandler(t,
		recorder.Snapshot().Subscriptions, "event.session.created.v1")

	return handler, cfg.store, ctx
}

// findSubscriptionHandler returns the bound EntryHandler for the named topic
// from a registry snapshot. Fails the test if no subscription for the topic
// is registered — that indicates auditappendsession wiring has drifted.
func findSubscriptionHandler(t *testing.T, subs []cell.SubscriptionRequest, topic string) outbox.EntryHandler {
	t.Helper()
	for _, sub := range subs {
		if sub.Spec.Topic == topic {
			if sub.Handler == nil {
				t.Fatalf("subscription handler for %q must be non-nil", topic)
				return nil
			}
			return sub.Handler
		}
	}
	t.Fatalf("no subscription found for topic %q (auditcore wiring drift)", topic)
	return nil
}

// fatalOnErr calls t.Fatalf if err is non-nil.
func fatalOnErr(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}
