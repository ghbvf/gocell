package configreceive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// stubConfigGetter is a test double for ports.ConfigGetter.
type stubConfigGetter struct {
	entry      ports.ConfigEntry
	err        error
	calledWith string // records the key argument passed to GetEntry
}

func (s *stubConfigGetter) GetEntry(_ context.Context, key string) (ports.ConfigEntry, error) {
	s.calledWith = key
	return s.entry, s.err
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

// callWithConfigEventOwner runs handler through ConfigEventMiddleware and returns
// (disposition, err) extracted from the HandleResult.
func callWithConfigEventOwner(
	collector obmetrics.ConfigEventCollector,
	entry outbox.Entry,
	fn outbox.EntryHandler,
) (outbox.Disposition, error) {
	wrapped := obmetrics.ConfigEventMiddleware(collector)(
		outbox.Subscription{Topic: entry.Topic, ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		fn,
	)
	result := wrapped(context.Background(), entry)
	return result.Disposition, result.Err
}

func TestHandleEntryUpserted_ValidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"metadata-only key+version+actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"}`)},
		{"higher version", []byte(`{"key":"jwt.ttl","version":42,"actorId":"admin-1"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-1",
				Topic:   TopicConfigEntryUpserted,
				Payload: tt.payload,
			}
			result := svc.HandleEntryUpserted(context.Background(), entry)
			assert.Equal(t, outbox.DispositionAck, result.Disposition)
			assert.NoError(t, result.Err)
		})
	}
}

func TestHandleEntryUpserted_InvalidPayload_PermanentError(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json{"), "unmarshal"},
		{"missing key", []byte(`{"version":1,"actorId":"admin-1"}`), "missing key"},
		{"empty key", []byte(`{"key":"","version":1,"actorId":"admin-1"}`), "missing key"},
		{"blank key whitespace", []byte(`{"key":"   ","version":1,"actorId":"admin-1"}`), "missing key"},
		{"missing version", []byte(`{"key":"jwt.ttl","actorId":"admin-1"}`), "invalid version"},
		{"invalid version zero", []byte(`{"key":"jwt.ttl","version":0,"actorId":"admin-1"}`), "invalid version"},
		{"missing actorId", []byte(`{"key":"jwt.ttl","version":1}`), "missing actorId"},
		{"empty actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":""}`), "missing actorId"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-bad",
				Topic:   TopicConfigEntryUpserted,
				Payload: tt.payload,
			}

			result := svc.HandleEntryUpserted(context.Background(), entry)
			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)
			assert.Contains(t, result.Err.Error(), tt.wantErr)

			var permErr *outbox.PermanentError
			assert.True(t, errors.As(result.Err, &permErr))
		})
	}
}

func TestHandleEntryDeleted_ValidPayload(t *testing.T) {
	svc := NewService(slog.Default())
	entry := outbox.Entry{
		ID:      "evt-del-1",
		Topic:   TopicConfigEntryDeleted,
		Payload: []byte(`{"key":"jwt.ttl","version":3,"actorId":"admin-1"}`),
	}
	result := svc.HandleEntryDeleted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestHandleEntryDeleted_InvalidPayload_PermanentError(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json{"), "unmarshal"},
		{"missing key", []byte(`{"version":1,"actorId":"admin-1"}`), "missing key"},
		{"missing version", []byte(`{"key":"jwt.ttl","actorId":"admin-1"}`), "invalid version"},
		{"version zero", []byte(`{"key":"jwt.ttl","version":0,"actorId":"admin-1"}`), "invalid version"},
		{"missing actorId", []byte(`{"key":"jwt.ttl","version":1}`), "missing actorId"},
		{"empty actorId", []byte(`{"key":"jwt.ttl","version":1,"actorId":""}`), "missing actorId"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-del-bad",
				Topic:   TopicConfigEntryDeleted,
				Payload: tt.payload,
			}

			result := svc.HandleEntryDeleted(context.Background(), entry)
			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Error(t, result.Err)
			assert.Contains(t, result.Err.Error(), tt.wantErr)

			var permErr *outbox.PermanentError
			assert.True(t, errors.As(result.Err, &permErr))
		})
	}
}

func TestTopicConstants(t *testing.T) {
	assert.Equal(t, "event.config.entry-upserted.v1", TopicConfigEntryUpserted)
	assert.Equal(t, "event.config.entry-deleted.v1", TopicConfigEntryDeleted)
}

func TestHandleEntryUpserted_DirectHandler_ValidPayload_Ack(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-direct-1",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"admin-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestHandleEntryUpserted_DirectHandler_InvalidJSON_Reject(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{ID: "evt-direct-2", Topic: TopicConfigEntryUpserted, Payload: []byte("bad{")}
	result := svc.HandleEntryUpserted(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

// TestHandleEntryUpserted_ValueField_Accepted locks down
// ADR-202605031600 v1 schema evolution: an extra "value" field on a
// metadata-only event payload must NOT be rejected at runtime. The
// metadata-only contract is enforced by producers (don't emit value);
// consumers ignore the extra field and Ack normally.
func TestHandleEntryUpserted_ValueField_Accepted(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-direct-3",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","value":"30m","version":1,"actorId":"admin-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition,
		"lenient consumer must Ack metadata-only events even when extra fields appear")
	assert.NoError(t, result.Err)
}

func TestHandleEntryUpserted_WithConfigGetter_FetchOK(t *testing.T) {
	stub := &stubConfigGetter{
		entry: ports.ConfigEntry{Key: "jwt.ttl", Value: "30m", Version: 2},
	}
	svc := NewService(slog.Default(), WithConfigGetter(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-1",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":2,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
	// F5: assert stub was called with the correct key from the event payload.
	assert.Equal(t, "jwt.ttl", stub.calledWith, "ConfigGetter.GetEntry must be called with the event's key")
}

// TestHandleEntryUpserted_WithConfigGetter_FetchError asserts that a transient
// fetch error (non-404) causes HandleEntryUpserted to return DispositionRequeue.
func TestHandleEntryUpserted_WithConfigGetter_FetchError(t *testing.T) {
	stub := &stubConfigGetter{
		err: errors.New("configcore unavailable"),
	}
	svc := NewService(slog.Default(), WithConfigGetter(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-2",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition, "transient fetch failure must trigger Requeue")
	assert.Error(t, result.Err)
	assert.Equal(t, "jwt.ttl", stub.calledWith, "ConfigGetter.GetEntry must be called with the event's key")
}

// TestHandleEntryUpserted_WithConfigGetter_FetchNotFound asserts that a 404
// (config entry genuinely gone) is treated as a stale event: log Warn + Ack,
// since retrying cannot help when the entry no longer exists.
func TestHandleEntryUpserted_WithConfigGetter_FetchNotFound(t *testing.T) {
	stub := &stubConfigGetter{
		err: errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config entry not found",
			errcode.WithCategory(errcode.CategoryDomain)),
	}
	svc := NewService(slog.Default(), WithConfigGetter(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-404",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition, "not-found fetch must Ack (stale event, no retry needed)")
	assert.NoError(t, result.Err)
}

func TestHandleEntryUpserted_WithoutConfigGetter_NoFetch(t *testing.T) {
	// Nil configGetter — service must function correctly in log-only mode.
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-cfg-3",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestHandleEntryUpserted_ConfigEventMetricsOutcomes(t *testing.T) {
	tests := []struct {
		name            string
		getter          ports.ConfigGetter
		payload         []byte
		wantDisposition outbox.Disposition
		wantRecords     []configEventRecord
	}{
		{
			name:            "valid upsert without getter records ack",
			payload:         []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
			wantDisposition: outbox.DispositionAck,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "getter success records ack",
			getter: &stubConfigGetter{entry: ports.ConfigEntry{
				Key: "jwt.ttl", Value: "30m", Version: 1,
			}},
			payload:         []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
			wantDisposition: outbox.DispositionAck,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name: "getter not found records stale",
			getter: &stubConfigGetter{err: errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "missing",
				errcode.WithCategory(errcode.CategoryDomain))},
			payload:         []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
			wantDisposition: outbox.DispositionAck,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonStale,
			}},
		},
		{
			name:            "invalid payload records permanent error",
			payload:         []byte(`not-json{`),
			wantDisposition: outbox.DispositionReject,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
		{
			name:            "transient getter error records no service outcome",
			getter:          &stubConfigGetter{err: errors.New("configcore unavailable")},
			payload:         []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
			wantDisposition: outbox.DispositionRequeue,
			wantRecords:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			opts := []Option{WithConfigEventCollector(collector)}
			if tt.getter != nil {
				opts = append(opts, WithConfigGetter(tt.getter))
			}
			svc := NewService(slog.Default(), opts...)
			entry := outbox.Entry{ID: "evt-metrics", Topic: TopicConfigEntryUpserted, Payload: tt.payload}

			disposition, _ := callWithConfigEventOwner(collector, entry, svc.HandleEntryUpserted)
			assert.Equal(t, tt.wantDisposition, disposition)
			assert.Equal(t, tt.wantRecords, collector.records)
		})
	}
}

// TestIsPermanentAuthFailure tests the isPermanentAuthFailure helper directly.
func TestIsPermanentAuthFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"plain error", errors.New("some error"), false},
		{"errcode ErrAuthUnauthorized", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "401"), true},
		{"errcode ErrAuthForbidden", errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "403"), true},
		{"errcode other code", errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "not found",
			errcode.WithCategory(errcode.CategoryDomain)), false},
		{"wrapped ErrAuthUnauthorized", fmt.Errorf("wrap: %w",
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "401")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPermanentAuthFailure(tt.err))
		})
	}
}

// TestHandleEntryUpserted_WithConfigGetter_PermanentAuth401 asserts that a 401
// from ConfigGetter causes DispositionReject + PermanentError.
func TestHandleEntryUpserted_WithConfigGetter_PermanentAuth401(t *testing.T) {
	stub := &stubConfigGetter{
		err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "service token rejected"),
	}
	svc := NewService(slog.Default(), WithConfigGetter(stub))

	entry := outbox.Entry{
		ID:      "evt-auth-401",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition, "401 must Reject (permanent)")
	require.Error(t, result.Err)

	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "must wrap PermanentError")
}

// TestHandleEntryUpserted_WithConfigGetter_PermanentAuth403 asserts that a 403
// from ConfigGetter causes DispositionReject + PermanentError.
func TestHandleEntryUpserted_WithConfigGetter_PermanentAuth403(t *testing.T) {
	stub := &stubConfigGetter{
		err: errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "caller_cell not in allowlist"),
	}
	svc := NewService(slog.Default(), WithConfigGetter(stub))

	entry := outbox.Entry{
		ID:      "evt-auth-403",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition, "403 must Reject (permanent)")
	require.Error(t, result.Err)

	var permErr *outbox.PermanentError
	assert.True(t, errors.As(result.Err, &permErr), "must wrap PermanentError")
}

// TestWithConfigEventCollector_NilCollector verifies that passing nil to
// WithConfigEventCollector sets the noop collector (branch coverage).
func TestWithConfigEventCollector_NilCollector(t *testing.T) {
	svc := NewService(slog.Default(), WithConfigEventCollector(nil))
	require.NotNil(t, svc)
	// Calling through the service must not panic (noop collector is set).
	entry := outbox.Entry{
		ID:      "evt-noop",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	result := svc.HandleEntryUpserted(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)
}

// TestRecordConfigEventProcess_NilCollector covers the nil-collector guard
// in recordConfigEventProcess (defensive branch that is not reachable through
// the public constructor but is reachable from the same-package test).
func TestRecordConfigEventProcess_NilCollector(t *testing.T) {
	// Construct a Service with a nil configEventCollector directly (bypassing
	// NewService which always installs the noop fallback). This is the only
	// way to exercise the nil-guard in recordConfigEventProcess.
	svc := &Service{
		logger:               slog.Default(),
		configEventCollector: nil, // intentionally nil — exercises nil guard
	}
	// Must not panic.
	svc.recordConfigEventProcess(context.Background(), obmetrics.ConfigEventProcessReasonAck)
}

func TestHandleEntryDeleted_ConfigEventMetricsOutcomes(t *testing.T) {
	tests := []struct {
		name            string
		payload         []byte
		wantDisposition outbox.Disposition
		wantRecords     []configEventRecord
	}{
		{
			name:            "valid delete records ack",
			payload:         []byte(`{"key":"jwt.ttl","version":3,"actorId":"adm-1"}`),
			wantDisposition: outbox.DispositionAck,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonAck,
			}},
		},
		{
			name:            "invalid delete records permanent error",
			payload:         []byte(`not-json{`),
			wantDisposition: outbox.DispositionReject,
			wantRecords: []configEventRecord{{
				cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonPermanentError,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &recordingConfigEventCollector{}
			svc := NewService(slog.Default(), WithConfigEventCollector(collector))
			entry := outbox.Entry{ID: "evt-del-metrics", Topic: TopicConfigEntryDeleted, Payload: tt.payload}

			disposition, _ := callWithConfigEventOwner(collector, entry, svc.HandleEntryDeleted)
			assert.Equal(t, tt.wantDisposition, disposition)
			assert.Equal(t, tt.wantRecords, collector.records)
		})
	}
}
