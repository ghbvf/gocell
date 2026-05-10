package auditappend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// directRunner is a test-only pass-through TxRunner for auditappend tests.
// Moved from service.go (Fix 4): directRunner is dead code in production
// (the cell-level demoTxRunner is injected instead); keeping it here in
// the test package makes the test-only intent explicit.
type directRunner struct{}

// Compile-time assertion: directRunner must satisfy persistence.TxRunner.
var _ persistence.TxRunner = directRunner{}

func (directRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestService(t testing.TB) (*Service, *mem.AuditRepository) {
	t.Helper()
	repo := mem.NewAuditRepository()
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(), WithClock(clock.Real()), WithTxManager(directRunner{}))
	require.NoError(t, err)
	return svc, repo
}

// assertAck asserts that HandleResult indicates a successful Ack disposition.
func assertAck(t testing.TB, got outbox.HandleResult) {
	t.Helper()
	assert.Equal(t, outbox.DispositionAck, got.Disposition, "expected DispositionAck, got %v (err=%v)", got.Disposition, got.Err)
	assert.NoError(t, got.Err)
}

// assertReject asserts that HandleResult indicates a permanent rejection (DLX path).
func assertReject(t testing.TB, got outbox.HandleResult) {
	t.Helper()
	assert.Equal(t, outbox.DispositionReject, got.Disposition, "expected DispositionReject, got %v", got.Disposition)
	assert.Error(t, got.Err)
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	repo := mem.NewAuditRepository()
	_, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(), WithClock(clock.Real()) /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

// TestNewService_HMACKeyTooShort locks the slice-layer wrapping contract for
// the domain.NewHashChain failure branch. Without this the cell-level error
// would surface only when AuditCore.Init() runs end-to-end, which obscures
// regressions to the constructor's error pass-through (e.g. an accidental
// fmt.Errorf wrapping that hides the *errcode.Error).
func TestNewService_HMACKeyTooShort(t *testing.T) {
	repo := mem.NewAuditRepository()
	shortKey := make([]byte, 31) // one short of RFC 2104 §3 minimum
	_, err := NewService(repo, shortKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()), WithTxManager(directRunner{}))
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "domain *errcode.Error must pass through unchanged")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "audit hmac key too short")
	// The slice constructor must not double-wrap the message — cell.initSlices
	// owns the "auditappend: %w" prefix.
	assert.NotContains(t, err.Error(), "auditappend: auditappend:",
		"slice constructor must not double-wrap; cell.initSlices owns the prefix")

	var sawMin, sawActual bool
	for _, attr := range ec.Details {
		switch attr.Key {
		case "minimumBytes":
			sawMin = true
			assert.Equal(t, int64(32), attr.Value.Int64())
		case "actualBytes":
			sawActual = true
			assert.Equal(t, int64(31), attr.Value.Int64())
		}
	}
	assert.True(t, sawMin, "details must include minimumBytes")
	assert.True(t, sawActual, "details must include actualBytes")
}

func TestService_HandleEvent(t *testing.T) {
	tests := []struct {
		name       string
		entry      outbox.Entry
		wantReject bool
		wantChain  int
	}{
		{
			name: "user created event",
			entry: outbox.Entry{
				ID:        "evt-1",
				EventType: "event.user.created.v1",
				Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "adm-1"}),
			},
			wantChain: 1,
		},
		{
			name: "session created event",
			entry: outbox.Entry{
				ID:        "evt-2",
				EventType: "event.session.created.v1",
				Payload:   mustJSON(t, map[string]any{"sessionId": "sess-1", "userId": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "config entry-upserted event with actorId",
			entry: outbox.Entry{
				ID:        "evt-3",
				EventType: "event.config.entry-upserted.v1",
				Payload:   mustJSON(t, map[string]any{"key": "app.name", "version": 1, "actorId": "adm-1"}),
			},
			wantChain: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService(t)

			result := svc.HandleEvent(context.Background(), tt.entry)
			if tt.wantReject {
				assertReject(t, result)
			} else {
				assertAck(t, result)
				assert.Equal(t, tt.wantChain, svc.ChainLen())
				assert.Equal(t, tt.wantChain, repo.Len())
			}
		})
	}
}

func TestService_HandleEvent_ChainGrows(t *testing.T) {
	svc, repo := newTestService(t)

	for i := range 5 {
		entry := outbox.Entry{
			ID:        "evt-" + string(rune('0'+i)),
			EventType: "event.user.created.v1",
			Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "adm-1"}),
		}
		assertAck(t, svc.HandleEvent(context.Background(), entry))
	}

	assert.Equal(t, 5, svc.ChainLen())
	assert.Equal(t, 5, repo.Len())
}

// TestService_HandleEvent_InvalidPayload_Reject asserts that an invalid JSON
// payload causes HandleEvent to return DispositionReject (DLX path) — permanent
// error that must not be retried.
func TestService_HandleEvent_InvalidPayload_Reject(t *testing.T) {
	repo := mem.NewAuditRepository()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewService(repo, testHMACKey, logger, clock.Real(), WithClock(clock.Real()), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-bad-json",
		EventType: "event.user.created.v1",
		Payload:   []byte("{invalid json}"),
	}

	result := svc.HandleEvent(context.Background(), entry)
	assertReject(t, result)

	var permErr *outbox.PermanentError
	require.ErrorAs(t, result.Err, &permErr, "Err must wrap a PermanentError so ConsumerBase routes to DLX")

	// Broker-controlled IDs (event_id, event_type) live on the structured log
	// line, not in the error message — see CWE-117 fix in service.go HandleEvent
	// invalid-JSON branch. Lock both fields plus the log level so a regression
	// (dropping event_type or downgrading to Debug) trips the test.
	logEntry := sloghelper.FindLogEntry(logBuf.String(), "audit-append: invalid JSON payload")
	require.NotNil(t, logEntry, "expected Warn log line for invalid JSON")
	assert.Equal(t, "WARN", logEntry["level"], "invalid JSON must emit at WARN level")
	assert.Equal(t, "evt-bad-json", logEntry["event_id"], "log line must carry event_id")
	assert.Equal(t, "event.user.created.v1", logEntry["event_type"], "log line must carry event_type")

	// Verify the chain was NOT appended — invalid payload must not pollute the audit chain.
	assert.Equal(t, 0, svc.ChainLen(), "invalid payload must not be appended to the audit chain")
	assert.Equal(t, 0, repo.Len(), "invalid payload must not be persisted")
}

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestService_HandleEvent_PublishError_DoesNotFailAppend(t *testing.T) {
	repo := mem.NewAuditRepository()
	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "auditcore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()),
		WithEmitter(emitter),
		WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-pub-err",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "adm-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assertAck(t, result)
	assert.Equal(t, 1, svc.ChainLen(), "entry should still be appended to chain")
}

// TestService_HandleEvent_ActorExtraction covers the actor-id extractor's
// priority on the success path: actorId (admin-write events) > userId
// (session.* events). Missing-actor handling is covered separately in
// TestService_HandleEvent_ActorMissing_Reject.
func TestService_HandleEvent_ActorExtraction(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		payload     map[string]any
		wantActorID string
	}{
		{
			name:        "camelCase userId (session.created)",
			eventType:   "event.session.created.v1",
			payload:     map[string]any{"sessionId": "sess-1", "userId": "usr-cam"},
			wantActorID: "usr-cam",
		},
		{
			name:        "actorId field (user.locked, PR-CFG-G1)",
			eventType:   "event.user.locked.v1",
			payload:     map[string]any{"actorId": "adm-1", "userId": "usr-snk"},
			wantActorID: "adm-1",
		},
		{
			name:        "actorId field (config.entry-upserted, PR-CFG-G1) — production path",
			eventType:   "event.config.entry-upserted.v1",
			payload:     map[string]any{"key": "k", "version": 1, "actorId": "adm-99"},
			wantActorID: "adm-99",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService(t)
			entry := outbox.Entry{
				ID:        "evt-" + tt.name,
				EventType: tt.eventType,
				Payload:   mustJSON(t, tt.payload),
			}
			assertAck(t, svc.HandleEvent(context.Background(), entry))

			entries, err := repo.GetRange(context.Background(), 0, 1)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, tt.wantActorID, entries[0].ActorID)
		})
	}
}

// TestService_HandleEvent_ActorMissing_Reject locks the fail-closed contract:
// when both actorId and userId are absent or empty, HandleEvent must return
// DispositionReject (DLX path) wrapping a PermanentError, and the audit chain
// must remain untouched. Producer-side schemas already declare these fields
// as required; reaching this branch indicates upstream contract violation
// and routing the event to DLX preserves audit traceability.
func TestService_HandleEvent_ActorMissing_Reject(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   map[string]any
	}{
		{
			name:      "config event missing actorId (no userId fallback)",
			eventType: "event.config.entry-upserted.v1",
			payload:   map[string]any{"key": "k", "version": 1},
		},
		{
			name:      "user event missing both actorId and userId",
			eventType: "event.user.created.v1",
			payload:   map[string]any{"username": "bob"},
		},
		{
			name:      "explicit empty strings for actorId and userId",
			eventType: "event.user.locked.v1",
			payload:   map[string]any{"actorId": "", "userId": ""},
		},
		{
			name:      "snake_case user_id is not a recognized field",
			eventType: "event.user.created.v1",
			payload:   map[string]any{"user_id": "usr-2", "username": "bob"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mem.NewAuditRepository()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			svc, err := NewService(repo, testHMACKey, logger, clock.Real(),
				WithClock(clock.Real()), WithTxManager(directRunner{}))
			require.NoError(t, err)

			entry := outbox.Entry{
				ID:        "evt-actor-missing",
				EventType: tt.eventType,
				Payload:   mustJSON(t, tt.payload),
			}

			result := svc.HandleEvent(context.Background(), entry)
			assertReject(t, result)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr,
				"Err must wrap a PermanentError so ConsumerBase routes to DLX")

			// Audit chain must NOT be polluted by rejected event.
			assert.Equal(t, 0, svc.ChainLen(), "rejected event must not be appended to chain")
			assert.Equal(t, 0, repo.Len(), "rejected event must not be persisted")

			// Log line carries event_id / event_type as structured fields and
			// must NOT contain the literal "system" fallback marker.
			logEntry := sloghelper.FindLogEntry(logBuf.String(), "audit-append: actor missing")
			require.NotNil(t, logEntry, "expected Warn log line for missing actor")
			assert.Equal(t, "WARN", logEntry["level"], "missing actor must emit at WARN level")
			assert.Equal(t, "evt-actor-missing", logEntry["event_id"], "log line must carry event_id")
			assert.Equal(t, tt.eventType, logEntry["event_type"], "log line must carry event_type")
			assert.NotContains(t, logBuf.String(), `"system"`,
				`fail-closed reject must not log the legacy "system" fallback marker`)
		})
	}
}

// failingAppendRepo wraps mem.AuditRepository but always returns a sentinel error on Append.
type failingAppendRepo struct {
	*mem.AuditRepository
	err error
}

func (f *failingAppendRepo) Append(_ context.Context, _ *domain.AuditEntry) error {
	return f.err
}

// TestService_HandleEvent_RepoAppendFails_Requeue covers the persistFn → repo.Append
// failure branch: a transient persistence error must produce DispositionRequeue
// so ConsumerBase can back off and retry.
func TestService_HandleEvent_RepoAppendFails_Requeue(t *testing.T) {
	sentinel := fmt.Errorf("db unavailable")
	repo := &failingAppendRepo{AuditRepository: mem.NewAuditRepository(), err: sentinel}

	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-repo-fail",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "adm-1"}),
	}

	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	require.Error(t, result.Err)
	assert.ErrorIs(t, result.Err, sentinel, "result.Err must wrap the sentinel repo error")
	// Lock the transient classification: persist failures must NOT be wrapped
	// as PermanentError (which would route to DLX instead of being retried).
	var permErr *outbox.PermanentError
	assert.False(t, errors.As(result.Err, &permErr), "transient persist error must NOT be PermanentError")
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
