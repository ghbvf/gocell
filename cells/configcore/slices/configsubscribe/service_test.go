package configsubscribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
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
	fn func(context.Context, outbox.Entry) error,
) error {
	var err error
	wrapped := obmetrics.ConfigEventMiddleware(collector)(
		outbox.Subscription{Topic: entry.Topic, ConsumerGroup: "configcore", CellID: "configcore", SliceID: "configsubscribe"},
		func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
			err = fn(ctx, entry)
			if err != nil {
				return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: err}
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		},
	)
	wrapped(context.Background(), entry)
	return err
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
			svc := NewService(slog.Default())

			for _, e := range tt.events {
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), e))
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
	svc := NewService(slog.Default())

	// v3 → v5 → v3 (replay): final state must be v5.
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))

	v, ok := svc.Cache().GetVersion("k")
	require.True(t, ok)
	assert.Equal(t, 5, v, "stale/replayed event must not overwrite higher version")
}

func TestService_HandleEntryDeleted(t *testing.T) {
	svc := NewService(slog.Default())
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))

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
	svc := NewService(slog.Default())

	// No prior upsert — delete must still succeed and record a tombstone.
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("nonexistent", 1)))
	assert.Equal(t, 0, svc.Cache().Len())
	v, present := svc.Cache().GetVersion("nonexistent")
	assert.False(t, present)
	assert.Equal(t, 1, v)
}

// TestService_Tombstone_ReplayedOlderUpsertRejected verifies the core protection:
// upsert v1 → delete v2 → replayed upsert v1 → cache stays tombstoned at v2.
func TestService_Tombstone_ReplayedOlderUpsertRejected(t *testing.T) {
	svc := NewService(slog.Default())

	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))
	// Replayed older upsert must be rejected.
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "replayed upsert must not resurrect a tombstoned key")
	assert.Equal(t, 2, v, "tombstone version must not be overwritten by stale upsert")
	assert.Equal(t, 0, svc.Cache().Len())
}

// TestService_Tombstone_ReplayedOlderDeleteRejected verifies symmetric replay
// protection on the delete side: upsert v3 → delete v2 (stale) → cache stays
// active at v3.
func TestService_Tombstone_ReplayedOlderDeleteRejected(t *testing.T) {
	svc := NewService(slog.Default())

	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	// Stale delete with older version must be dropped.
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))

	v, present := svc.Cache().GetVersion("k")
	assert.True(t, present, "stale delete must not tombstone a newer active entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 1, svc.Cache().Len())
}

// TestService_Tombstone_DeleteThenHigherUpsertRestores verifies the recovery
// path: delete v2 → upsert v3 → cache becomes active at v3.
func TestService_Tombstone_DeleteThenHigherUpsertRestores(t *testing.T) {
	svc := NewService(slog.Default())

	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 2)))
	// A new upsert with version > tombstone restores the entry.
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))

	v, present := svc.Cache().GetVersion("k")
	assert.True(t, present, "upsert after delete with higher version must restore entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 1, svc.Cache().Len())
}

// TestService_GetVersion_AfterDelete_ReturnsTombstoneVersion explicitly asserts
// the tombstone semantics of GetVersion: present=false, version=tombstone version.
// The delete event carries the same version as the last upsert (normal producer path).
func TestService_GetVersion_AfterDelete_ReturnsTombstoneVersion(t *testing.T) {
	svc := NewService(slog.Default())

	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	// Normal delete: version equals the last upsert version (V >= known → accepted).
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 5)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "GetVersion must return present=false after delete")
	assert.Equal(t, 5, v, "GetVersion must return the tombstone version")
}

// TestService_Tombstone_SameVersionDeleteAccepted verifies that a delete event
// carrying the same version as the last upsert is accepted and tombstones the entry.
// This is the normal producer path: Delete returns the row's current version, so
// the delete event version == last upsert version.
func TestService_Tombstone_SameVersionDeleteAccepted(t *testing.T) {
	svc := NewService(slog.Default())

	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	// delete at same version as existing upsert: V >= known → accepted as tombstone.
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k", 3)))

	v, present := svc.Cache().GetVersion("k")
	assert.False(t, present, "same-version delete must tombstone the entry")
	assert.Equal(t, 3, v)
	assert.Equal(t, 0, svc.Cache().Len())
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
			svc := NewService(slog.Default())
			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tt.payload}

			err := svc.HandleEntryUpserted(context.Background(), entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, 0, svc.Cache().Len())

			var permErr *outbox.PermanentError
			require.ErrorAs(t, err, &permErr)
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
			svc := NewService(slog.Default())
			require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("existing.key", 1)))

			entry := outbox.Entry{ID: "bad-delete", Topic: domain.TopicConfigEntryDeleted, Payload: tt.payload}
			err := svc.HandleEntryDeleted(context.Background(), entry)
			require.Error(t, err)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, err, &permErr)
			assert.Equal(t, 1, svc.Cache().Len(), "cache must be unchanged after invalid delete")
			_, ok := svc.Cache().GetVersion("existing.key")
			require.True(t, ok)
		})
	}
}

// TestWrapLegacyHandler_Reject_Cases covers payloads that must still be
// rejected under ADR-202605031600. Lenient consumers tolerate extra fields,
// but invalid JSON and missing required fields remain permanent failures.
func TestWrapLegacyHandler_Reject_Cases(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid json", []byte("not-json")},
		{"missing actorId", []byte(`{"key":"k","version":1}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tc.payload}
			result := handler(context.Background(), entry)

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
		wantErr     bool
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
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 2)))
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
			wantErr: true,
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			svc := NewService(slog.Default(), WithConfigEventCollector(collector))
			if tt.arrange != nil {
				tt.arrange(svc)
				collector.records = nil
			}

			err := callWithConfigEventOwner(collector, tt.entry, svc.HandleEntryUpserted)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
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
		wantErr     bool
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
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
			},
			entry: makeEntryDeleted("app.name", 3),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "older delete records stale",
			arrange: func(svc *Service) {
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
			},
			entry: makeEntryDeleted("app.name", 2),
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonStale,
			}},
		},
		{
			name: "replayed same-version tombstone records stale",
			arrange: func(svc *Service) {
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("app.name", 3)))
				require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("app.name", 3)))
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
			wantErr: true,
			wantRecords: []configEventRecord{{
				cell: "configcore", slice: "configsubscribe", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			svc := NewService(slog.Default(), WithConfigEventCollector(collector))
			if tt.arrange != nil {
				tt.arrange(svc)
				collector.records = nil
			}

			err := callWithConfigEventOwner(collector, tt.entry, svc.HandleEntryDeleted)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantRecords, collector.records)
		})
	}
}
