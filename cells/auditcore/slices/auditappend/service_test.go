package auditappend

import (
	"context"
	"encoding/json"
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
				Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "session created event",
			entry: outbox.Entry{
				ID:        "evt-2",
				EventType: "event.session.created.v1",
				Payload:   mustJSON(map[string]any{"sessionId": "sess-1", "userId": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "config entry-upserted event (no userId → system actor)",
			entry: outbox.Entry{
				ID:        "evt-3",
				EventType: "event.config.entry-upserted.v1",
				Payload:   mustJSON(map[string]any{"key": "app.name", "value": "v", "version": 1}),
			},
			wantChain: 1,
		},
		{
			name: "user created event with snake_case user_id (transitional)",
			entry: outbox.Entry{
				ID:        "evt-4",
				EventType: "event.user.created.v1",
				Payload:   mustJSON(map[string]any{"user_id": "usr-2", "username": "bob"}),
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
			Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
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
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(), WithClock(clock.Real()), WithTxManager(directRunner{}))
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
	assert.Contains(t, result.Err.Error(), "evt-bad-json", "error message must contain event ID")

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
		Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assertAck(t, result)
	assert.Equal(t, 1, svc.ChainLen(), "entry should still be appended to chain")
}

// TestService_HandleEvent_ActorExtraction covers the actor-id extractor's
// priority: actorId (preferred) > userId (fallback) > "system" (default).
// G.6 migrated user events from snake_case user_id to camelCase userId; G.2
// added required actorId to all admin-write events.
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
		{
			name:        "no actor field (legacy config event) → system fallback",
			eventType:   "event.config.entry-upserted.v1",
			payload:     map[string]any{"key": "k", "version": 1},
			wantActorID: "system",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService(t)
			entry := outbox.Entry{
				ID:        "evt-" + tt.name,
				EventType: tt.eventType,
				Payload:   mustJSON(tt.payload),
			}
			assertAck(t, svc.HandleEvent(context.Background(), entry))

			entries, err := repo.GetRange(context.Background(), 0, 1)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, tt.wantActorID, entries[0].ActorID)
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

// TestService_HandleEvent_RepoAppendFails_Requeue covers the persistFn→repo.Append
// failure branch (service.go:180-187): a transient persistence error must produce
// DispositionRequeue so ConsumerBase can back off and retry.
func TestService_HandleEvent_RepoAppendFails_Requeue(t *testing.T) {
	sentinel := fmt.Errorf("db unavailable")
	repo := &failingAppendRepo{AuditRepository: mem.NewAuditRepository(), err: sentinel}

	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-repo-fail",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
	}

	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	require.Error(t, result.Err)
	assert.ErrorIs(t, result.Err, sentinel, "result.Err must wrap the sentinel repo error")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
