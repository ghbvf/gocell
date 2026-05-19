//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// buildAuditcoreChain wires a docker-free auditcore Cell at the seam that
// J-auditlogintrail criteria observe: the auditappendsession subscription
// declared in cells/auditcore/cell.go (line ~162) and its underlying
// ledger.Store. It returns the bound EntryHandler, the in-memory Store
// (so callers can probe Tail/Verify), the cell context, and an
// outbox.Entry pre-built with a canonical event.session.created.v1
// payload that satisfies appender.ActorAcceptUserFallback (userId
// present).
//
// The seam is the same one used by cells/auditcore/cell_test.go
// newTestCell — we re-build it here because tests/integration cannot
// import internal helpers from cells/auditcore. ledger.Protocol,
// MemStore, and the wiring options are public surface area.
//
// ref: cells/auditcore/cell_test.go newTestCell (in-package mirror).
func buildAuditcoreChain(t *testing.T) (
	handler outbox.EntryHandler,
	store ledger.Store,
	ctx context.Context,
	entry outbox.Entry,
) {
	t.Helper()

	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "namespace parse")

	proto, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "protocol")

	memStore, err := ledger.NewMemStore(proto, clock.Real())
	require.NoError(t, err, "memstore")

	c := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithLedgerProtocol(proto),
		auditcore.WithLedgerStore(memStore),
		auditcore.WithEmitter(outbox.NewNoopEmitter()),
		auditcore.WithTxManager(cell.DemoCellTxManager()),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	ctx = context.Background()
	recorder := cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)
	require.NoError(t, c.Init(ctx, recorder), "auditcore.Init")

	handler = findSubscriptionHandler(t,
		recorder.Snapshot().Subscriptions, "event.session.created.v1")

	// session.created payload shape — anonymous struct mirrors
	// cells/accesscore/internal/dto.SessionCreatedEvent. The dto package is
	// under cells/accesscore/internal/ and unreachable from tests/integration;
	// the canonical schema lives in contracts/event/session/created/v1/
	// payload.schema.json (independent of any Go type). Per cell-patterns.md
	// "跨 cell decode 重复属于预期成本", we duplicate the shape here rather
	// than introduce a cross-cell shared Go type.
	// ref: cells/accesscore/internal/dto/session_events.go SessionCreatedEvent
	payload, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		UserID    string `json:"userId"`
	}{
		SessionID: "sess-j-auditlogintrail",
		UserID:    "usr-j-auditlogintrail",
	})
	require.NoError(t, err, "marshal session.created payload")

	entry = outbox.Entry{
		ID:        "evt-j-auditlogintrail",
		EventType: "event.session.created.v1",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	return handler, memStore, ctx, entry
}

// findSubscriptionHandler returns the bound EntryHandler for the named
// topic from a registry snapshot. Fails the test if no subscription on
// the topic is present — that signals the auditappendsession slice's
// declarative subscription has drifted away from event.session.created.v1.
func findSubscriptionHandler(t *testing.T, subs []cell.SubscriptionRequest, topic string) outbox.EntryHandler {
	t.Helper()
	for _, sub := range subs {
		if sub.Spec.Topic == topic {
			require.NotNilf(t, sub.Handler,
				"subscription handler for %q must be non-nil", topic)
			return sub.Handler
		}
	}
	t.Fatalf("no subscription found for topic %q (auditcore wiring drift)", topic)
	return nil
}
