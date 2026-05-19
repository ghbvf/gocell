//go:build integration

package integration

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestJConfighotreloadConfigPublish implements journeys/J-confighotreload.yaml
// passCriteria "配置写入后发布 config.entry-upserted / config.entry-deleted 状态同步事件"
// — checkRef journey.J-confighotreload.config-publish.
//
// J-confighotreload is lifecycle: active (promoted from experimental in this PR),
// so governance VERIFY-06 runs this test inside `gocell validate --strict`.
// The test MUST be Docker-free — it drives the configcore cell's configsubscribe
// slice subscription seam (RegistryRecorder.Snapshot().Subscriptions) rather than
// going through broker delivery.
//
// The criterion asserts that configcore's configsubscribe slice correctly consumes
// event.config.entry-upserted.v1 and event.config.entry-deleted.v1 events:
//   - HandleEntryUpserted Acks and updates the internal cache (version=1, present=true)
//   - HandleEntryDeleted Acks and tombstones the cache (version=1, present=false)
//
// Layer compromise: we drive the subscription seam (cell.RegistryRecorder.
// Snapshot().Subscriptions[topic].Handler) instead of going through broker delivery,
// because broker delivery is owned by adapters/rabbitmq's integration suite and is
// Docker-bound. The seam IS the contract for "configcore consumes the event".
//
// Cache assertion strategy: configsubscribe.Service.Cache() is a public method
// returning *configsubscribe.Cache; GetVersion(key) directly reads the internal
// map. We construct the Service independently (not via the cell field) to access
// Cache() without importing cells/configcore/internal/. The cell init path is
// exercised by the subscription-registration assertion below.
//
// ref: tests/integration/journey_auditlogintrail_event_consume_test.go (same pattern).
func TestJConfighotreloadConfigPublish(t *testing.T) {
	t.Parallel()

	// Silence demo-mode lifecycle WARN/INFO logs ("using cell.DemoCellTxManager",
	// "configsubscribe: cache updated") so VERIFY-06's `gocell validate --strict`
	// execution under -v doesn't surface them as pseudo-anomalies. Real
	// operational health for configcore is exercised by cells/configcore/cell_test.go
	// and tests/integration/l2atomicity/, which use their own logger fixtures.
	// ref: journey_auditlogintrail_helpers_test.go:79-84 (same rationale).
	testLogger := slog.New(slog.DiscardHandler)

	// --- Cell subscription registration: verify configcore declares both subscriptions ---
	t.Run("cell_registers_subscriptions", func(t *testing.T) {
		c := configcore.NewConfigCore(
			configcore.WithClock(clock.Real()),
			configcore.WithInMemoryDefaults(),
			configcore.WithEmitter(outbox.NewNoopEmitter()),
			configcore.WithTxManager(cell.DemoCellTxManager()),
			configcore.WithMetricsProvider(metrics.NopProvider{}),
			configcore.WithLogger(testLogger),
		)
		ctx := context.Background()
		recorder := cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)
		require.NoError(t, c.Init(ctx, recorder), "configcore.Init")
		subs := recorder.Snapshot().Subscriptions
		assertConfighotreloadSubscription(t, subs, "event.config.entry-upserted.v1")
		assertConfighotreloadSubscription(t, subs, "event.config.entry-deleted.v1")
	})

	// Construct configsubscribe.Service directly to access Cache().GetVersion()
	// without importing cells/configcore/internal/ (Go internal-package barrier).
	// The handler methods (HandleEntryUpserted / HandleEntryDeleted) are public
	// and satisfy outbox.EntryHandler.
	svc := configsubscribe.NewService(testLogger,
		configsubscribe.WithClock(clock.Real()),
	)
	ctx := context.Background()
	upsertHandler := svc.HandleEntryUpserted
	deleteHandler := svc.HandleEntryDeleted

	const testKey = "jwt.ttl"

	// --- sub-test: upsert event Acks + cache version becomes 1, present=true ---
	t.Run("upsert_acks_and_updates_cache", func(t *testing.T) {
		entry := makeUpsertEntry(t, testKey, 1)
		result := upsertHandler(ctx, entry)
		require.Equalf(t, outbox.DispositionAck, result.Disposition,
			"configsubscribe must Ack entry-upserted; got disposition=%v error=%v",
			result.Disposition, result.Err)
		// Direct cache state assertion: proves handler updated internal state,
		// not just returned Ack on a no-op path.
		version, present := svc.Cache().GetVersion(testKey)
		require.True(t, present, "cache entry must be present after upsert")
		require.Equal(t, 1, version, "cache version must equal upserted version")

		// Stale replay (same version=1) must Ack without changing version.
		staleResult := upsertHandler(ctx, makeUpsertEntry(t, testKey, 1))
		require.Equalf(t, outbox.DispositionAck, staleResult.Disposition,
			"configsubscribe must Ack stale replay; got disposition=%v error=%v",
			staleResult.Disposition, staleResult.Err)
		v2, p2 := svc.Cache().GetVersion(testKey)
		require.True(t, p2, "cache must remain present after stale replay")
		require.Equal(t, 1, v2, "stale replay must not bump cache version")

		// Newer version=2 must be accepted and version updated.
		newerResult := upsertHandler(ctx, makeUpsertEntry(t, testKey, 2))
		require.Equalf(t, outbox.DispositionAck, newerResult.Disposition,
			"configsubscribe must Ack newer version; got disposition=%v error=%v",
			newerResult.Disposition, newerResult.Err)
		v3, p3 := svc.Cache().GetVersion(testKey)
		require.True(t, p3, "cache must remain present after newer upsert")
		require.Equal(t, 2, v3, "cache version must equal newer upserted version")
	})

	// --- sub-test: delete event Acks + cache tombstoned (present=false) ---
	t.Run("delete_acks_and_tombstones_cache", func(t *testing.T) {
		const delKey = testKey + "-del"
		// Setup: upsert so the key is known.
		upsertResult := upsertHandler(ctx, makeUpsertEntry(t, delKey, 1))
		require.Equalf(t, outbox.DispositionAck, upsertResult.Disposition,
			"setup upsert must Ack; got disposition=%v error=%v",
			upsertResult.Disposition, upsertResult.Err)
		v1, p1 := svc.Cache().GetVersion(delKey)
		require.True(t, p1, "key must be present after setup upsert")
		require.Equal(t, 1, v1, "setup upsert version must be 1")

		// Delete event: cache must become tombstoned.
		deleteResult := deleteHandler(ctx, makeDeleteEntry(t, delKey, 1))
		require.Equalf(t, outbox.DispositionAck, deleteResult.Disposition,
			"configsubscribe must Ack entry-deleted; got disposition=%v error=%v",
			deleteResult.Disposition, deleteResult.Err)
		v2, p2 := svc.Cache().GetVersion(delKey)
		require.False(t, p2, "cache entry must be tombstoned (present=false) after delete")
		require.Equal(t, 1, v2, "tombstone version must equal deleted version")
	})

	// --- sub-test: invalid payload Rejects ---
	t.Run("invalid_payload_rejects", func(t *testing.T) {
		badEntry := outbox.Entry{
			ID:        "evt-bad",
			EventType: "event.config.entry-upserted.v1",
			Payload:   []byte(`{not-json`),
		}
		result := upsertHandler(ctx, badEntry)
		require.Equalf(t, outbox.DispositionReject, result.Disposition,
			"invalid payload must Reject (dead letter); got disposition=%v", result.Disposition)
	})
}
