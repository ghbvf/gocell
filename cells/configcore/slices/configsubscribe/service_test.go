package configsubscribe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

const (
	// testTTL is the tombstone TTL used by Batch B/C GC tests.
	testTTL = 24 * time.Hour
	// testHalfTTL is the half-TTL advance used to test pre-expiry sweep.
	testHalfTTL = 12 * time.Hour
	// testOverTTL is the advance beyond TTL (13h) to cross the 24h boundary.
	testOverTTL = 13 * time.Hour
	// testFarFuture advances far beyond TTL to verify active entries are not evicted.
	testFarFuture = 100 * 24 * time.Hour
	// testSubDefaultTTL is a tombstone TTL below the minimum (1h) for warn tests.
	testSubDefaultTTL = 1 * time.Hour
	// testNegativeTTL is a non-positive TTL that must fall back to defaultTombstoneTTL.
	testNegativeTTL = -5 * time.Second
	// testGCEventuallyTimeout is the Eventually timeout for GC goroutine observation.
	testGCEventuallyTimeout = 2 * time.Second
	// testGCEventuallyTick is the polling tick for GC goroutine observation.
	// 10ms is intentionally looser than the 5ms used in cell_lifecycle_test.go
	// (a different package); the two files cannot share consts. The slice-level
	// tests tolerate a slightly wider polling window because they exercise
	// white-box cache internals with additional lock contention.
	testGCEventuallyTick = 10 * time.Millisecond
)

// makeEntryUpserted builds a metadata-only outbox.Entry for entry-upserted.
// The payload carries only key+version+actorId — no value field.
func makeEntryUpserted(key string, version int) outbox.Entry {
	payload, _ := json.Marshal(configevents.EntryUpserted{
		Key:     key,
		Version: version,
		ActorID: "admin-test",
	})
	return outbox.Entry{ID: "test-upsert", Topic: domain.TopicConfigEntryUpserted, Payload: payload}
}

// makeEntryDeleted builds an outbox.Entry for entry-deleted with the given version.
func makeEntryDeleted(key string, version int) outbox.Entry {
	payload, _ := json.Marshal(configevents.EntryDeleted{Key: key, Version: version, ActorID: "admin-test"})
	return outbox.Entry{ID: "test-delete", Topic: domain.TopicConfigEntryDeleted, Payload: payload}
}

// requireAck asserts that the handler result is a successful Ack with no error.
func requireAck(t *testing.T, result outbox.HandleResult) {
	t.Helper()
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

type recordingConfigEventCollector struct {
	records []configEventRecord
}

type configEventRecord struct {
	cell   string
	slice  string
	reason obmetrics.ConfigEventProcessReason
}

func (c *recordingConfigEventCollector) RecordEventProcess(cellID, sliceID string, reason obmetrics.ConfigEventProcessReason) {
	c.records = append(c.records, configEventRecord{cell: cellID, slice: sliceID, reason: reason})
}

func (c *recordingConfigEventCollector) RecordEventSettlement(string, string, string, outbox.SettlementResult) {
}

func callWithConfigEventOwner(
	collector obmetrics.ConfigEventCollector,
	entry outbox.Entry,
	fn func(context.Context, outbox.Entry) outbox.HandleResult,
) outbox.HandleResult {
	var result outbox.HandleResult
	wrapped := obmetrics.ConfigEventMiddleware(collector)(
		outbox.Subscription{Topic: entry.Topic, ConsumerGroup: "configcore", CellID: "configcore", SliceID: "configsubscribe"},
		func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			result = fn(ctx, entry)
			return result
		},
	)
	wrapped(context.Background(), entry)
	return result
}

func TestService_HandleEntryUpserted(t *testing.T) {
	tests := []struct {
		name        string
		events      []outbox.Entry
		wantKey     string
		wantVersion int
		wantPresent bool
		wantLen     int
	}{
		{
			name:        "created state updates cache",
			events:      []outbox.Entry{makeEntryUpserted("app.name", 1)},
			wantKey:     "app.name",
			wantVersion: 1,
			wantPresent: true,
			wantLen:     1,
		},
		{
			name: "updated state updates cache to latest version",
			events: []outbox.Entry{
				makeEntryUpserted("k", 1),
				makeEntryUpserted("k", 2),
			},
			wantKey:     "k",
			wantVersion: 2,
			wantPresent: true,
			wantLen:     1,
		},
		{
			name:        "version 5 is tracked",
			events:      []outbox.Entry{makeEntryUpserted("timeout", 5)},
			wantKey:     "timeout",
			wantVersion: 5,
			wantPresent: true,
			wantLen:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))

			for _, e := range tt.events {
				requireAck(t, svc.HandleEntryUpserted(context.Background(), e))
			}

			assert.Equal(t, tt.wantLen, svc.Cache().Len())
			v, ok := svc.Cache().GetVersion(tt.wantKey)
			require.Equal(t, tt.wantPresent, ok)
			assert.Equal(t, tt.wantVersion, v)
		})
	}
}

// TestService_HandleEntryUpserted_Monotonicity verifies that stale or replayed
// events (version <= known version) are ignored without overwriting the cache.
func TestService_HandleEntryUpserted_Monotonicity(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	// v3 → v5 → v3 (replay): final state must be v5.
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))

	v, ok := svc.Cache().GetVersion("k")
	require.True(t, ok)
	assert.Equal(t, 5, v, "stale/replayed event must not overwrite higher version")
}

func TestService_HandleEntryDeleted(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))

	// Len counts only active entries; after delete, Len must be 0.
	assert.Equal(t, 0, svc.Cache().Len())
	// GetVersion still returns the tombstone version, but present=false.
	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "tombstoned key must return present=false")
	assert.Equal(t, 2, v, "tombstone must record the delete version")
}

// TestService_HandleEntryDeleted_NonExistentKey verifies that deleting a key that
// was never seen records a tombstone and does not error.
func TestService_HandleEntryDeleted_NonExistentKey(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	// No prior upsert — delete must still succeed and record a tombstone.
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("nonexistent", 1)))
	assert.Equal(t, 0, svc.Cache().Len())
	v, present := svc.Cache().GetVersion("nonexistent")
	assert.False(t, present)
	assert.Equal(t, 1, v)
}

// TestService_Tombstone_ReplayedOlderUpsertRejected verifies the core protection:
// upsert v1 → delete v2 → replayed upsert v1 → cache stays tombstoned at v2.
func TestService_Tombstone_ReplayedOlderUpsertRejected(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))
	// Replayed older upsert must be rejected (silently — returns Ack because stale).
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "replayed upsert must not resurrect a tombstoned key")
	assert.Equal(t, 2, v, "tombstone version must not be overwritten by stale upsert")
	assert.Equal(t, 0, svc.Cache().Len())
}

// TestService_Tombstone_ReplayedOlderDeleteRejected verifies symmetric replay
// protection on the delete side: upsert v3 → delete v2 (stale) → cache stays
// active at v3.
func TestService_Tombstone_ReplayedOlderDeleteRejected(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	// Stale delete with older version must be dropped (silently — returns Ack).
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))

	v, present := svc.Cache().GetVersion("k")
	assert.True(t, present, "stale delete must not tombstone a newer active entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 1, svc.Cache().Len())
}

// TestService_Tombstone_DeleteThenHigherUpsertRestores verifies the recovery
// path: delete v2 → upsert v3 → cache becomes active at v3.
func TestService_Tombstone_DeleteThenHigherUpsertRestores(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))
	// A new upsert with version > tombstone restores the entry.
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))

	v, present := svc.Cache().GetVersion("k")
	assert.True(t, present, "upsert after delete with higher version must restore entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 1, svc.Cache().Len())
}

// TestService_GetVersion_AfterDelete_ReturnsTombstoneVersion explicitly asserts
// the tombstone semantics of GetVersion: present=false, version=tombstone version.
// The delete event carries the same version as the last upsert (normal producer path).
func TestService_GetVersion_AfterDelete_ReturnsTombstoneVersion(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	// Normal delete: version equals the last upsert version (V >= known → accepted).
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 5)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "GetVersion must return present=false after delete")
	assert.Equal(t, 5, v, "GetVersion must return the tombstone version")
}

// TestService_Tombstone_SameVersionDeleteAccepted verifies that a delete event
// carrying the same version as the last upsert is accepted and tombstones the entry.
// This is the normal producer path: Delete returns the row's current version, so
// the delete event version == last upsert version.
func TestService_Tombstone_SameVersionDeleteAccepted(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	// delete at same version as existing upsert: V >= known → accepted as tombstone.
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 3)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "same-version delete must tombstone the entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 0, svc.Cache().Len())
}

// --- Three-state HandleResult tests (Ack / Reject / Requeue) ---

// TestHandleEntryUpserted_HappyPath_Ack verifies the happy path returns DispositionAck.
func TestHandleEntryUpserted_HappyPath_Ack(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))
	result := svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1))
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
	assert.Equal(t, 1, svc.Cache().Len())
}

// TestHandleEntryUpserted_InvalidPayload_Reject verifies that unparseable payloads
// return DispositionReject with a PermanentError (routes to DLX, no retry).
func TestHandleEntryUpserted_InvalidPayload_Reject(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantMsg string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing actorId", []byte(`{"key":"k","version":1}`), "missing actorId"},
		{"missing key", []byte(`{"version":1,"actorId":"a"}`), "missing key"},
		{"invalid version zero", []byte(`{"key":"k","version":0,"actorId":"a"}`), "invalid version"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))
			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tc.payload}
			result := svc.HandleEntryUpserted(context.Background(), entry)

			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)
			assert.Contains(t, result.Err.Error(), tc.wantMsg)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr)

			assert.Equal(t, 0, svc.Cache().Len())
		})
	}
}

// TestHandleEntryUpserted_StaleVersion_Ack verifies that a stale (lower-version)
// replay returns DispositionAck (silently dropped, not requeued).
func TestHandleEntryUpserted_StaleVersion_Ack(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))
	// Seed version 5.
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	// Stale replay (version 3 <= 5) — returns Ack, not an error.
	result := svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3))
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

// TestHandleEntryDeleted_HappyPath_Ack verifies the happy path returns DispositionAck.
func TestHandleEntryDeleted_HappyPath_Ack(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	result := svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2))
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
	assert.Equal(t, 0, svc.Cache().Len())
}

// TestHandleEntryDeleted_InvalidPayload_Reject verifies that unparseable delete payloads
// return DispositionReject with a PermanentError (routes to DLX, no retry).
func TestHandleEntryDeleted_InvalidPayload_Reject(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid json", []byte("not-json")},
		{"missing key", []byte(`{"version":1,"actorId":"a"}`)},
		{"missing version", []byte(`{"key":"k","actorId":"a"}`)},
		{"version zero", []byte(`{"key":"k","version":0,"actorId":"a"}`)},
		{"missing actorId", []byte(`{"key":"k","version":1}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))
			entry := outbox.Entry{ID: "bad-delete", Topic: domain.TopicConfigEntryDeleted, Payload: tc.payload}
			result := svc.HandleEntryDeleted(context.Background(), entry)

			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr)
		})
	}
}

// TestHandleEntryDeleted_RepoErr_Requeue: in-memory cache does not error;
// stale deletes return Ack (silently dropped).
func TestHandleEntryDeleted_RepoErr_Requeue(t *testing.T) {
	svc := NewService(slog.Default(), WithClock(clock.Real()))
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	// Stale delete (version 3 < 5) — returns Ack, not an error.
	result := svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 3))
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestService_HandleEntryUpserted_InvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing key", []byte(`{"version":1,"actorId":"a"}`), "missing key"},
		{"invalid version zero", []byte(`{"key":"k","version":0,"actorId":"a"}`), "invalid version"},
		{"missing actorId", []byte(`{"key":"k","version":1}`), "missing actorId"},
		{"empty actorId", []byte(`{"key":"k","version":1,"actorId":""}`), "missing actorId"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))
			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tt.payload}

			result := svc.HandleEntryUpserted(context.Background(), entry)
			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)
			assert.Contains(t, result.Err.Error(), tt.wantErr)
			assert.Equal(t, 0, svc.Cache().Len())

			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr)
		})
	}
}

func TestService_HandleEntryDeleted_InvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing key", []byte(`{"version":1,"actorId":"a"}`), "missing key"},
		{"missing version", []byte(`{"key":"existing.key","actorId":"a"}`), "invalid version"},
		{"version zero", []byte(`{"key":"existing.key","version":0,"actorId":"a"}`), "invalid version"},
		{"missing actorId", []byte(`{"key":"existing.key","version":1}`), "missing actorId"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))
			requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("existing.key", 1)))

			entry := outbox.Entry{ID: "bad-delete", Topic: domain.TopicConfigEntryDeleted, Payload: tt.payload}
			result := svc.HandleEntryDeleted(context.Background(), entry)
			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr)
			assert.Equal(t, 1, svc.Cache().Len(), "cache must be unchanged after invalid delete")
			_, ok := svc.Cache().GetVersion("existing.key")
			require.True(t, ok)
		})
	}
}

// TestHandleEntryUpserted_Reject_Cases covers payloads that must be
// rejected under ADR-202605031600. Lenient consumers tolerate extra fields,
// but invalid JSON and missing required fields remain permanent failures.
func TestHandleEntryUpserted_Reject_Cases(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid json", []byte("not-json")},
		{"missing actorId", []byte(`{"key":"k","version":1}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(slog.Default(), WithClock(clock.Real()))
			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tc.payload}
			result := svc.HandleEntryUpserted(context.Background(), entry)

			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			assert.Error(t, result.Err)
		})
	}
}

func TestService_ConfigEventMetrics_EntryUpsertedOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		arrange     func(*Service)
		entry       outbox.Entry
		wantReject  bool
		wantRecords []configEventRecord
	}{
		{
			name:  "accepted upsert records ack",
			entry: makeEntryUpserted("app.name", 1),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "replayed older upsert records stale",
			arrange: func(svc *Service) {
				requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 2)))
			},
			entry: makeEntryUpserted("app.name", 1),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonStale,
			}},
		},
		{
			name: "invalid upsert records permanent error",
			entry: outbox.Entry{
				ID: "bad", Topic: domain.TopicConfigEntryUpserted,
				Payload: []byte(`not-json{`),
			},
			wantReject: true,
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			svc := NewService(slog.Default(), WithClock(clock.Real()), WithConfigEventCollector(collector))
			if tt.arrange != nil {
				tt.arrange(svc)
				collector.records = nil
			}

			result := callWithConfigEventOwner(collector, tt.entry, svc.HandleEntryUpserted)
			if tt.wantReject {
				assert.Equal(t, outbox.DispositionReject, result.Disposition)
			} else {
				assert.Equal(t, outbox.DispositionAck, result.Disposition)
			}
			assert.Equal(t, tt.wantRecords, collector.records)
		})
	}
}

func TestService_ConfigEventMetrics_EntryDeletedOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		arrange     func(*Service)
		entry       outbox.Entry
		wantReject  bool
		wantRecords []configEventRecord
	}{
		{
			name:  "accepted delete records ack",
			entry: makeEntryDeleted("app.name", 1),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "same-version delete records ack",
			arrange: func(svc *Service) {
				requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
			},
			entry: makeEntryDeleted("app.name", 3),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "older delete records stale",
			arrange: func(svc *Service) {
				requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
			},
			entry: makeEntryDeleted("app.name", 2),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonStale,
			}},
		},
		{
			name: "replayed same-version tombstone records stale",
			arrange: func(svc *Service) {
				requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
				requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("app.name", 3)))
			},
			entry: makeEntryDeleted("app.name", 3),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonStale,
			}},
		},
		{
			name: "invalid delete records permanent error",
			entry: outbox.Entry{
				ID: "bad-delete", Topic: domain.TopicConfigEntryDeleted,
				Payload: []byte(`not-json{`),
			},
			wantReject: true,
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			svc := NewService(slog.Default(), WithClock(clock.Real()), WithConfigEventCollector(collector))
			if tt.arrange != nil {
				tt.arrange(svc)
				collector.records = nil
			}

			result := callWithConfigEventOwner(collector, tt.entry, svc.HandleEntryDeleted)
			if tt.wantReject {
				assert.Equal(t, outbox.DispositionReject, result.Disposition)
			} else {
				assert.Equal(t, outbox.DispositionAck, result.Disposition)
			}
			assert.Equal(t, tt.wantRecords, collector.records)
		})
	}
}

// ---------------------------------------------------------------------------
// Local spy for EventbusCacheCollector — mirrors recordingConfigEventCollector style
// ---------------------------------------------------------------------------

type recordingEventbusCacheCollector struct {
	count atomic.Int64
	calls []struct{ cellID, sliceID string }
	mu    sync.Mutex
}

func (r *recordingEventbusCacheCollector) RecordTombstoneEvicted(cellID, sliceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count.Add(1)
	r.calls = append(r.calls, struct{ cellID, sliceID string }{cellID, sliceID})
}

func (r *recordingEventbusCacheCollector) total() int64 { return r.count.Load() }

// ---------------------------------------------------------------------------
// Batch B: tombstone TTL + lifecycle-bound GC goroutine tests
// ---------------------------------------------------------------------------

// TestCache_SweepTombstones_RemovesExpiredOnly verifies that sweepTombstones
// removes only tombstone entries whose age exceeds tombstoneTTL, and that the
// monotonic guard for live keys is fully preserved.
func TestCache_SweepTombstones_RemovesExpiredOnly(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	spy := &recordingEventbusCacheCollector{}
	svc := NewService(slog.Default(),
		WithClock(fc),
		WithTombstoneTTL(testTTL),
		WithEventbusCacheCollector(spy),
	)

	// Upsert keyA v1, then delete → tombstone at v2.
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("keyA", 1)))
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("keyA", 2)))

	// Advance only 12h — tombstone not yet expired.
	fc.Advance(testHalfTTL)
	svc.cache.sweepTombstones(fc.Now())

	// keyA still present as tombstone.
	v, present := svc.Cache().GetVersion("keyA")
	assert.False(t, present, "tombstone must survive before TTL expires")
	assert.Equal(t, 2, v, "tombstone version must be preserved")

	// Replayed older upsert v1 must still be rejected (monotonic guard intact).
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("keyA", 1)))
	v, present = svc.Cache().GetVersion("keyA")
	assert.False(t, present, "replayed upsert must not resurrect tombstone within TTL")
	assert.Equal(t, 2, v)

	assert.Equal(t, int64(0), spy.total(), "no evictions expected before TTL expires")

	// Advance 13h more (total 25h > 24h TTL) then sweep.
	fc.Advance(testOverTTL)
	svc.cache.sweepTombstones(fc.Now())

	// keyA is now gone.
	v, present = svc.Cache().GetVersion("keyA")
	assert.False(t, present, "evicted tombstone must not be found")
	assert.Equal(t, 0, v, "evicted tombstone must return zero version")

	assert.GreaterOrEqual(t, spy.total(), int64(1), "at least one RecordTombstoneEvicted expected")
	spy.mu.Lock()
	if len(spy.calls) > 0 {
		assert.Equal(t, "configcore", spy.calls[0].cellID)
		assert.Equal(t, "configsubscribe", spy.calls[0].sliceID)
	}
	spy.mu.Unlock()
}

// TestCache_SweepTombstones_NeverTouchesActive verifies that active (present=true)
// entries are never evicted by sweepTombstones, preserving the monotonic guard.
func TestCache_SweepTombstones_NeverTouchesActive(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	spy := &recordingEventbusCacheCollector{}
	svc := NewService(slog.Default(),
		WithClock(fc),
		WithTombstoneTTL(testTTL),
		WithEventbusCacheCollector(spy),
	)

	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("keyB", 5)))

	// Advance far beyond TTL.
	fc.Advance(testFarFuture)
	svc.cache.sweepTombstones(fc.Now())

	// keyB must still be present at v5.
	v, present := svc.Cache().GetVersion("keyB")
	assert.True(t, present, "active entry must never be evicted")
	assert.Equal(t, 5, v, "active entry version must be preserved")

	// Monotonic guard still intact: replay v3 rejected.
	requireAck(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("keyB", 3)))
	v, present = svc.Cache().GetVersion("keyB")
	assert.True(t, present)
	assert.Equal(t, 5, v, "stale replay must not overwrite active entry")

	assert.Equal(t, int64(0), spy.total(), "active entries must never trigger eviction metric")
}

// TestService_TombstoneGC_GoroutineLifecycle verifies the full lifecycle of the
// background GC goroutine: start, tick-driven sweep, idempotent stop, and
// proper behavior when GC is disabled (ttl=0).
func TestService_TombstoneGC_GoroutineLifecycle(t *testing.T) {
	interval := testTTL / gcSweepDivisor // 12h

	fc := clockmock.New(time.Unix(0, 0))
	spy := &recordingEventbusCacheCollector{}
	svc := NewService(slog.Default(),
		WithClock(fc),
		WithTombstoneTTL(testTTL),
		WithEventbusCacheCollector(spy),
	)

	// Delete keyC → tombstone.
	requireAck(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("keyC", 1)))
	v, present := svc.Cache().GetVersion("keyC")
	assert.False(t, present)
	assert.Equal(t, 1, v)

	// Start GC.
	svc.StartTombstoneGC()

	// Wait for the GC goroutine to create its ticker.
	assert.Eventually(t, func() bool {
		return fc.PendingTickers() >= 1
	}, testGCEventuallyTimeout, testGCEventuallyTick, "GC goroutine must create a ticker")

	// Advance past the tombstone TTL across two tick intervals.
	// First tick at 12h: tombstone is 12h old (not yet expired).
	fc.Advance(interval) // 12h
	// Second tick at 24h: tombstone is now exactly at TTL boundary.
	// Advance a bit more to cross 24h TTL.
	fc.Advance(interval + time.Second) // 12h+1s = 24h+1s total

	// The GC goroutine should pick up the tick and sweep keyC.
	assert.Eventually(t, func() bool {
		v, present := svc.Cache().GetVersion("keyC")
		return !present && v == 0
	}, testGCEventuallyTimeout, testGCEventuallyTick, "GC goroutine must evict expired tombstone")

	// Verify metric was recorded.
	assert.GreaterOrEqual(t, spy.total(), int64(1))

	// Stop GC — must not error.
	require.NoError(t, svc.StopTombstoneGC(context.Background()))

	// Idempotent: second stop returns nil.
	require.NoError(t, svc.StopTombstoneGC(context.Background()))

	// --- defense-in-depth guard: tombstoneTTL forced to 0 via white-box write ---
	// NewService normalization guarantees tombstoneTTL > 0 in all normal paths, so
	// the StartTombstoneGC tombstoneTTL<=0 guard is unreachable in practice.
	// This sub-test exercises that defensive branch by force-writing the field
	// directly (same-package white-box access) and asserts that StartTombstoneGC
	// becomes a noop — no ticker is ever created.
	//
	// Note: WithTombstoneTTL(0) does NOT disable GC; it yields tombstoneTTL=24h
	// (default) because NewService normalizes 0 → defaultTombstoneTTL. That
	// behavior is verified separately by TestNewService_TombstoneTTLDefaultAndWarn.
	svcGuard := NewService(slog.Default(),
		WithClock(fc),
		WithTombstoneTTL(testTTL),
	)
	// Force the defensive branch by setting tombstoneTTL=0 after construction.
	svcGuard.cache.tombstoneTTL = 0
	svcGuard.StartTombstoneGC() // must be a noop — guard fires
	assert.Never(t, func() bool {
		return fc.PendingTickers() > 0
	}, testGCEventuallyTimeout, testGCEventuallyTick,
		"StartTombstoneGC guard (tombstoneTTL<=0) must never create a ticker")
	// Safe to call StopTombstoneGC — goroutine was never started.
	require.NoError(t, svcGuard.StopTombstoneGC(context.Background()))

	// Never-started service: StopTombstoneGC returns nil.
	svcNeverStarted := NewService(slog.Default(), WithClock(clock.Real()))
	require.NoError(t, svcNeverStarted.StopTombstoneGC(context.Background()))
}

// TestNewService_TombstoneTTLDefaultAndWarn verifies TTL normalization and the
// below-minimum warning log.
func TestNewService_TombstoneTTLDefaultAndWarn(t *testing.T) {
	// Default (no WithTombstoneTTL): effective ttl must be 24h.
	svcDefault := NewService(slog.Default(), WithClock(clock.Real()))
	assert.Equal(t, defaultTombstoneTTL, svcDefault.cache.tombstoneTTL,
		"default tombstoneTTL must be 24h")

	// WithTombstoneTTL(1h): effective ttl == 1h, warn emitted.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc1h := NewService(logger, WithClock(clock.Real()), WithTombstoneTTL(testSubDefaultTTL))
	assert.Equal(t, testSubDefaultTTL, svc1h.cache.tombstoneTTL,
		"explicit sub-default TTL must be stored as-is")
	logOutput := buf.String()
	assert.Contains(t, logOutput, "tombstone_ttl", "warn must include tombstone_ttl field")
	assert.Contains(t, logOutput, "min_recommended", "warn must include min_recommended field")

	// WithTombstoneTTL(-5) → default.
	svcNeg := NewService(slog.Default(), WithClock(clock.Real()), WithTombstoneTTL(testNegativeTTL))
	assert.Equal(t, defaultTombstoneTTL, svcNeg.cache.tombstoneTTL,
		"non-positive TTL must fall back to default")

	// WithTombstoneTTL(0) → default.
	svcZero := NewService(slog.Default(), WithClock(clock.Real()), WithTombstoneTTL(0))
	assert.Equal(t, defaultTombstoneTTL, svcZero.cache.tombstoneTTL,
		"zero TTL must fall back to default")
}

// TestStopTombstoneGC_CtxTimeout verifies the context-deadline branch of
// StopTombstoneGC: when the provided ctx is already canceled, StopTombstoneGC
// returns context.Canceled without waiting for the GC goroutine to drain.
//
// The test is deterministic — no real sleeps — because the ctx is pre-canceled
// before the Stop call, so the <-ctx.Done() arm wins immediately.
//
// Goroutine-leak safety: StopTombstoneGC internally calls cancel() on the GC
// goroutine's context before selecting on ctx.Done(), so the goroutine will
// exit independently after the call returns. We wait for PendingTickers to
// drop to 0 as evidence that the goroutine has exited before the test ends,
// preventing a false positive from the package-level goleak TestMain.
func TestStopTombstoneGC_CtxTimeout(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	svc := NewService(slog.Default(),
		WithClock(fc),
		WithTombstoneTTL(testTTL),
	)

	svc.StartTombstoneGC()

	// Wait for the GC goroutine to actually start and create its ticker.
	require.Eventually(t, func() bool {
		return fc.PendingTickers() >= 1
	}, testGCEventuallyTimeout, testGCEventuallyTick,
		"GC goroutine must be running before we test ctx-timeout stop")

	// Use a pre-canceled context so the <-ctx.Done() arm wins immediately.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.StopTombstoneGC(canceledCtx)
	require.Error(t, err, "StopTombstoneGC with canceled ctx must return non-nil error")
	assert.ErrorIs(t, err, context.Canceled)

	// StopTombstoneGC called cancel() on the GC goroutine's context before
	// returning, so the goroutine will exit shortly. Wait for it to drain so
	// the goleak TestMain does not report a false leak.
	assert.Eventually(t, func() bool {
		return fc.PendingTickers() == 0
	}, testGCEventuallyTimeout, testGCEventuallyTick,
		"GC goroutine must exit after its context is canceled")
}
