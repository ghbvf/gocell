package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/ghbvf/gocell/runtime/eventbus"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardPublisher is a minimal outbox.Publisher for wiring tests.
// It discards all published messages without error.
type discardPublisher struct{}

func (discardPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = discardPublisher{}

// TestBuildConfigCoreOpts_InMemoryMode_NoRelay asserts that memory topology
// returns a nil ManagedResource (no PG pool, no relay). No database
// connection is attempted; this test requires no external services.
//
// Regression guard for A11: if the relay is accidentally wired in memory mode
// it would try to Start() without a real DB and either panic or block.
func TestBuildConfigCoreOpts_InMemoryMode_NoRelay(t *testing.T) {
	t.Setenv("GOCELL_PG_DSN", "") // ensure no PG connection is attempted

	ctx := context.Background()
	topo := bootstrap.Topology{StorageBackend: "memory"}
	res, opts, err := buildConfigCoreOpts(ctx, topo, discardPublisher{}, metrics.NopProvider{}, crypto.NoopTransformer{})

	require.NoError(t, err)
	assert.Nil(t, res, "in-memory mode must not create a ManagedResource (no PG pool, no relay)")
	assert.NotEmpty(t, opts, "in-memory mode must return cell options (WithInMemoryDefaults)")
}

// TestBuildConfigCoreOpts_UnknownMode_Error asserts that an unrecognised
// StorageBackend (bypassing Topology validation) returns an error and a nil
// ManagedResource. In production, TopologyFromEnv already rejects such
// values; this test locks the defence-in-depth behaviour.
func TestBuildConfigCoreOpts_UnknownMode_Error(t *testing.T) {
	ctx := context.Background()
	topo := bootstrap.Topology{StorageBackend: "cassandra"}
	res, _, err := buildConfigCoreOpts(ctx, topo, discardPublisher{}, metrics.NopProvider{}, crypto.NoopTransformer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cassandra")
	assert.Nil(t, res, "error path must not leak a ManagedResource")
}

// TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil asserts that postgres mode
// produces a non-nil ManagedResource with a relay worker. This test requires a
// running PostgreSQL instance (GOCELL_PG_DSN must be set); it skips gracefully if
// the DSN is absent so it does not block unit test suites.
//
// The assertion that res != nil is the critical regression check for A11: before
// the fix, buildConfigCoreOpts never returned a relay, so the bootstrap could not
// register it.
func TestBuildConfigCoreOpts_PGMode_ManagedResourceNonNil(t *testing.T) {
	pgDSN, ok := os.LookupEnv("GOCELL_PG_DSN")
	if !ok || pgDSN == "" {
		t.Skip("GOCELL_PG_DSN not set; skipping PG-mode relay wiring test")
	}

	ctx := context.Background()
	topo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"}
	res, opts, err := buildConfigCoreOpts(ctx, topo, discardPublisher{}, metrics.NopProvider{}, crypto.NoopTransformer{})

	require.NoError(t, err, "postgres mode must not error when DSN is valid")
	require.NotNil(t, res, "postgres mode must return a non-nil ManagedResource (wraps pool + relay)")
	assert.NotEmpty(t, opts, "postgres mode must return cell options")

	// ManagedResource must have a non-nil relay worker (A11 fix).
	assert.NotNil(t, res.Worker(), "postgres ManagedResource must carry a relay worker")

	// Close the pool via ManagedResource.Close so pool.Close() is called.
	require.NoError(t, res.Close())
}

// TestTopologyAdapterInfo_TableDriven locks the adapter_info map shape that
// appears in /readyz?verbose and adapter_info metrics. This replaces the old
// TestBuildAdapterInfo_TableDriven which tested the now-deleted buildAdapterInfo
// function. The same semantics are now provided by Topology.AdapterInfo().
func TestTopologyAdapterInfo_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		adapterMode    string
		storageBackend string
		wantMode       string
		wantStorage    string
		wantOutbox     string
	}{
		{
			name:           "dev memory",
			adapterMode:    "",
			storageBackend: "memory",
			wantMode:       "in-memory",
			wantStorage:    "in-memory",
			wantOutbox:     "in-memory",
		},
		{
			name:           "postgres real",
			adapterMode:    "real",
			storageBackend: "postgres",
			wantMode:       "real-keys-postgres-storage",
			wantStorage:    "postgres",
			wantOutbox:     "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := bootstrap.Topology{
				AdapterMode:    tt.adapterMode,
				StorageBackend: tt.storageBackend,
			}
			info := topo.AdapterInfo()
			assert.Equal(t, tt.wantMode, info["mode"])
			assert.Equal(t, tt.wantStorage, info["storage"])
			assert.Equal(t, tt.wantOutbox, info["outbox_storage"])
			// event_bus stays in-memory — the relay forwards PG outbox entries
			// INTO the in-process bus, it does not replace it.
			assert.Equal(t, "in-memory", info["event_bus"])
		})
	}
}

// TestValidateModeCoupling_Matrix ensures the control/data-plane guard
// accepts compatible pairs and rejects postgres-without-real configurations
// that would run production persistence with dev-grade keys.
func TestValidateModeCoupling_Matrix(t *testing.T) {
	tests := []struct {
		name, cellAdapterMode, adapterMode string
		wantErr                            bool
	}{
		{"memory-dev", "memory", "", false},
		{"memory-real", "memory", "real", false},
		{"postgres-real", "postgres", "real", false},
		{"postgres-dev-rejected", "postgres", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModeCoupling(tt.cellAdapterMode, tt.adapterMode)
			if tt.wantErr {
				require.Error(t, err, "postgres without real adapterMode must fail-fast")
				assert.Contains(t, err.Error(), "postgres")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestOutboxE2E_CrossCellFanout is the P0 regression guard for the cross-cell
// fanout bug: before Commit 1, all cells in core-bundle shared a single
// ConsumerGroup ("core-bundle"), causing the idempotency key to be the same for
// every cell. The second cell to process an event saw ClaimDone and silently
// Acked without calling its handler.
//
// This test uses the in-memory eventbus (no Docker required) to verify that
// two subscribers with DIFFERENT ConsumerGroups on the same topic both receive
// and process the published event independently.
//
// Chain: eventbus.Publish → fanout dispatch → access-group handler called +
//
//	audit-group handler called (both independently, no shared idempotency namespace)
func TestOutboxE2E_CrossCellFanout(t *testing.T) {
	const topic = "test.fanout.cross-cg.v1"

	eb := eventbus.New()
	t.Cleanup(func() { _ = eb.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two counters — one per consumer group.
	var accessCalls, auditCalls atomic.Int64

	// Subscribe with ConsumerGroup "access-core" (simulates cells/access-core).
	accessSub := outbox.Subscription{Topic: topic, ConsumerGroup: "access-core"}
	go func() {
		_ = eb.Subscribe(ctx, accessSub, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			accessCalls.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Subscribe with ConsumerGroup "audit-core" (simulates cells/audit-core).
	auditSub := outbox.Subscription{Topic: topic, ConsumerGroup: "audit-core"}
	go func() {
		_ = eb.Subscribe(ctx, auditSub, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			auditCalls.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait until both subscribe goroutines have registered (Finding F5: replace
	// fixed sleep with explicit ready signal from eb.Ready).
	select {
	case <-eb.Ready(accessSub):
	case <-ctx.Done():
		t.Fatal("timed out waiting for access-core subscription to be ready")
	}
	select {
	case <-eb.Ready(auditSub):
	case <-ctx.Done():
		t.Fatal("timed out waiting for audit-core subscription to be ready")
	}

	// Publish exactly 1 event wrapped in a v1 envelope so the bus envelope
	// schema check (fail-closed, P1-14 A1/A2) accepts it.
	entry := outboxrt.ClaimedEntry{
		Entry: outbox.Entry{
			ID:        "e2e-fanout-1",
			EventType: topic,
			Topic:     topic,
			Payload:   []byte(`{"action":"fanout_test","key":"cross-cg","value":"ok"}`),
		},
	}
	envelope, err := outboxrt.MarshalEnvelope(entry)
	require.NoError(t, err, "MarshalEnvelope must not fail")
	require.NoError(t, eb.Publish(ctx, topic, envelope))

	// Assert both handlers are called exactly once.
	// Before the Subscription-first-class refactor (Commit 1), the shared
	// "core-bundle" ConsumerGroup caused the second cell to see ClaimDone and
	// silently Ack without calling its handler — one of the two counts would
	// stay at 0.
	require.Eventually(t, func() bool {
		return accessCalls.Load() == 1 && auditCalls.Load() == 1
	}, 3*time.Second, 5*time.Millisecond,
		"P0 regression: both consumer groups must receive the event; "+
			"access=%d audit=%d", accessCalls.Load(), auditCalls.Load())

	assert.Equal(t, int64(1), accessCalls.Load(), "access-core handler must be called exactly once")
	assert.Equal(t, int64(1), auditCalls.Load(), "audit-core handler must be called exactly once")
}
