package appender_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

type failingStore struct {
	ledger.Store
	err error
}

func (f *failingStore) Append(_ context.Context, _ *ledger.Entry) error { return f.err }

type directRunner struct{}

var _ persistence.TxRunner = directRunner{}

func (directRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	return p
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func newSpec(t *testing.T, name string, mode appender.ActorMode) appender.Spec {
	t.Helper()
	return appender.MustNewSpec(name, mode)
}

func newService(t *testing.T, spec appender.Spec, store ledger.Store, p *ledger.Protocol, opts ...appender.Option) *appender.Service {
	t.Helper()
	defaultOpts := []appender.Option{appender.WithTxManager(persistence.WrapForCell(directRunner{}))}
	svc, err := appender.NewService(spec, store, p, slog.Default(), clock.Real(), append(defaultOpts, opts...)...)
	require.NoError(t, err)
	return svc
}

// TestNewService_TxRunnerRequired covers the OUTBOX-SERVICE-01 fail-fast on
// nil TxRunner contract.
func TestNewService_TxRunnerRequired(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)

	_, err = appender.NewService(spec, store, p, slog.Default(), clock.Real())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	// Const-literal message (MESSAGE-CONST-LITERAL-01); slice identity flows
	// through WithDetails so wire output stays PII-safe.
	assert.Equal(t, "auditappender: TxRunner required; use WithTxManager", ec.Message)
	assertDetail(t, ec, "slice", "auditappenduser")
}

// assertDetail asserts a slog.Attr key/value pair appears in ec.Details.
func assertDetail(t *testing.T, ec *errcode.Error, key, want string) {
	t.Helper()
	for _, attr := range ec.Details {
		if attr.Key == key {
			assert.Equal(t, want, attr.Value.String(), "errcode detail %q value", key)
			return
		}
	}
	t.Fatalf("errcode detail %q not present (got %v)", key, ec.Details)
}

// TestActorExtraction is the strategy decision table for the two ActorMode
// branches. ActorAcceptUserFallback (user/config/session): actorId > userId.
// ActorRequireExplicit (role): actorId only.
func TestActorExtraction(t *testing.T) {
	cases := []struct {
		name        string
		spec        appender.Spec
		payload     map[string]any
		wantAck     bool
		wantActorID string
	}{
		{
			name:        "fallback_actorId_only",
			spec:        appender.MustNewSpec("auditappenduser", appender.ActorAcceptUserFallback),
			payload:     map[string]any{"actorId": "admin-1"},
			wantAck:     true,
			wantActorID: "admin-1",
		},
		{
			name:        "fallback_userId_only",
			spec:        appender.MustNewSpec("auditappenduser", appender.ActorAcceptUserFallback),
			payload:     map[string]any{"userId": "usr-1"},
			wantAck:     true,
			wantActorID: "usr-1",
		},
		{
			name:        "fallback_both_prefers_actorId",
			spec:        appender.MustNewSpec("auditappenduser", appender.ActorAcceptUserFallback),
			payload:     map[string]any{"actorId": "admin-1", "userId": "usr-1"},
			wantAck:     true,
			wantActorID: "admin-1",
		},
		{
			name:    "fallback_neither_rejects",
			spec:    appender.MustNewSpec("auditappenduser", appender.ActorAcceptUserFallback),
			payload: map[string]any{"username": "alice"},
			wantAck: false,
		},
		{
			name:        "explicit_actorId_only_accepts",
			spec:        appender.MustNewSpec("auditappendrole", appender.ActorRequireExplicit),
			payload:     map[string]any{"actorId": "admin-1", "userId": "usr-1"},
			wantAck:     true,
			wantActorID: "admin-1",
		},
		{
			name:    "explicit_userId_only_rejects",
			spec:    appender.MustNewSpec("auditappendrole", appender.ActorRequireExplicit),
			payload: map[string]any{"userId": "usr-1"},
			wantAck: false,
		},
		{
			name:    "explicit_neither_rejects",
			spec:    appender.MustNewSpec("auditappendrole", appender.ActorRequireExplicit),
			payload: map[string]any{"foo": "bar"},
			wantAck: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProtocol(t)
			store, err := ledger.NewMemStore(p, clock.Real())
			require.NoError(t, err)
			svc := newService(t, tc.spec, store, p)

			entry := outbox.Entry{
				ID:        "evt-" + tc.name,
				EventType: "event.test.v1",
				Payload:   mustJSON(t, tc.payload),
			}
			result := svc.HandleEvent(context.Background(), entry)
			if tc.wantAck {
				assert.Equal(t, outbox.DispositionAck, result.Disposition)
				tail, err := store.Tail(context.Background())
				require.NoError(t, err)
				assert.EqualValues(t, 1, tail.EntryCount)
				return
			}
			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			var permErr *outbox.PermanentError
			require.ErrorAs(t, result.Err, &permErr)
		})
	}
}

func TestService_HandleEvent_InvalidJSON_Reject(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)
	svc := newService(t, spec, store, p)

	entry := outbox.Entry{
		ID:        "evt-bad-json",
		EventType: "event.user.created.v1",
		Payload:   []byte("{invalid json}"),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	var permErr *outbox.PermanentError
	require.ErrorAs(t, result.Err, &permErr)
}

func TestService_HandleEvent_AppendFails_Requeue(t *testing.T) {
	sentinel := fmt.Errorf("db unavailable")
	p := newTestProtocol(t)
	realStore, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)
	svc := newService(t, spec, &failingStore{Store: realStore, err: sentinel}, p)

	entry := outbox.Entry{
		ID:        "evt-fail",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "admin-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	assert.ErrorIs(t, result.Err, sentinel)

	var permErr *outbox.PermanentError
	assert.False(t, errors.As(result.Err, &permErr), "transient persist error must NOT be PermanentError")
}

// TestService_HandleEvent_AppendFails_Classified verifies the disposition
// 收口: a positively-classified transient store error is Requeued (retry),
// while a positively-permanent error short-circuits to Reject → DLX instead
// of blind-Requeuing and burning the whole retry budget.
func TestService_HandleEvent_AppendFails_Classified(t *testing.T) {
	cases := []struct {
		name        string
		storeErr    error
		wantDisp    outbox.Disposition
		wantPermErr bool
	}{
		{
			name: "transient WrapInfra (PG serialization) → Requeue",
			storeErr: errcode.WrapInfra(errcode.Code("ERR_ADAPTER_PG_QUERY"),
				"serialization failure", errors.New("40001")),
			wantDisp: outbox.DispositionRequeue,
		},
		{
			name: "positively-permanent validation error → Reject → DLX",
			storeErr: errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit schema drift", errcode.WithCategory(errcode.CategoryValidation)),
			wantDisp:    outbox.DispositionReject,
			wantPermErr: true,
		},
		{
			// context.Canceled is infra (IsInfraError true) and NOT transient
			// → predicate !IsTransient && !IsInfraError == false → Requeue.
			// Locks the fail-closed direction: a future change to the
			// predicate that drops the IsInfraError clause would wrongly
			// Reject canceled-context store failures.
			name:     "context.Canceled (infra, not transient) → Requeue",
			storeErr: context.Canceled,
			wantDisp: outbox.DispositionRequeue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProtocol(t)
			realStore, err := ledger.NewMemStore(p, clock.Real())
			require.NoError(t, err)
			spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)
			svc := newService(t, spec, &failingStore{Store: realStore, err: tc.storeErr}, p)

			entry := outbox.Entry{
				ID:        "evt-cls",
				EventType: "event.user.created.v1",
				Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "actorId": "admin-1"}),
			}
			result := svc.HandleEvent(context.Background(), entry)
			assert.Equal(t, tc.wantDisp, result.Disposition)

			var permErr *outbox.PermanentError
			assert.Equal(t, tc.wantPermErr, errors.As(result.Err, &permErr))
		})
	}
}

// TestService_HandleEvent_Happy verifies the full happy path: ledger.Append +
// outbox.Emit run inside the same RunInTx block (L2 OutboxFact pattern). We
// observe the emit by injecting a recording emitter.
func TestService_HandleEvent_Happy(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	rec := &recordingEmitter{}
	spec := newSpec(t, "auditappendsession", appender.ActorAcceptUserFallback)
	svc := newService(t, spec, store, p, appender.WithEmitter(rec))

	entry := outbox.Entry{
		ID:        "evt-happy",
		EventType: "event.session.created.v1",
		Payload:   mustJSON(t, map[string]any{"actorId": "user-1", "userId": "user-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition)

	tail, err := store.Tail(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, tail.EntryCount)
	require.Len(t, rec.emitted, 1, "L2 OutboxFact: one outbox.Emit per Append")
	assert.Equal(t, "event.audit.appended.v1", rec.emitted[0].topic)
}

// recordingEmitter captures every Emit call. Implements outbox.Emitter.
type recordingEmitter struct {
	emitted []emittedRecord
}

type emittedRecord struct {
	topic   string
	payload []byte
}

func (r *recordingEmitter) Emit(_ context.Context, entry outbox.Entry) error {
	r.emitted = append(r.emitted, emittedRecord{topic: entry.EventType, payload: entry.Payload})
	return nil
}

// captureStore wraps a ledger.Store and records every Append call so tests
// can inspect the ledger.Entry that was actually written.
type captureStore struct {
	ledger.Store
	appended []*ledger.Entry
}

func (c *captureStore) Append(ctx context.Context, e *ledger.Entry) error {
	c.appended = append(c.appended, e)
	return c.Store.Append(ctx, e)
}

// newServiceWithLogBuf constructs a Service using a fakeClock and a JSON slog buffer
// so tests can inspect Warn-level log output.
func newServiceWithLogBuf(
	t *testing.T, spec appender.Spec, store ledger.Store, p *ledger.Protocol, fc *clockmock.FakeClock,
) (*appender.Service, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := appender.NewService(spec, store, p, logger, fc, appender.WithTxManager(persistence.WrapForCell(directRunner{})))
	require.NoError(t, err)
	return svc, buf
}

// TestHandleEvent_UsesEntryCreatedAt asserts that the ledger.Entry.Timestamp is
// set to outbox.Entry.CreatedAt (the event's original creation time), NOT to
// clk.Now() at handle time.
//
// F-02 RED: current implementation uses s.clk.Now(); after the fix it must use
// entry.CreatedAt so that audit timestamps faithfully represent when the business
// event occurred, not when it was picked up by the relay.
func TestHandleEvent_UsesEntryCreatedAt(t *testing.T) {
	p := newTestProtocol(t)
	inner, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	cap := &captureStore{Store: inner}
	spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)
	// Advance the fake clock so clk.Now() ≠ entry.CreatedAt
	fc.Advance(testtime.D10min)

	t1 := epoch // original event creation time, before clock advance
	entry := outbox.Entry{
		ID:        "evt-created-at",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"actorId": "actor-1"}),
		CreatedAt: t1,
	}

	svc, _ := newServiceWithLogBuf(t, spec, cap, p, fc)
	result := svc.HandleEvent(context.Background(), entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"HandleEvent must succeed for valid entry")
	require.Len(t, cap.appended, 1, "must have appended exactly one entry")

	gotTS := cap.appended[0].Timestamp
	// F-02 assertion: must use entry.CreatedAt, not clk.Now()
	if !gotTS.Equal(t1) {
		t.Errorf("Timestamp mismatch: got %v, want entry.CreatedAt=%v (clk.Now()=%v); "+
			"F-02: HandleEvent must use entry.CreatedAt, not clk.Now()",
			gotTS, t1, fc.Now())
	}
}

// TestHandleEvent_ZeroCreatedAt_FallbackToClk_LogsWarn asserts that when
// outbox.Entry.CreatedAt is zero, the service falls back to clk.Now() AND
// emits a Warn-level log record.
//
// F-02 RED: current implementation uses clk.Now() unconditionally (no fallback
// branch, no Warn log). After the fix: zero CreatedAt → Warn + clk.Now().
func TestHandleEvent_ZeroCreatedAt_FallbackToClk_LogsWarn(t *testing.T) {
	p := newTestProtocol(t)
	inner, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	cap := &captureStore{Store: inner}
	spec := newSpec(t, "auditappenduser", appender.ActorAcceptUserFallback)

	epoch := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fc := clockmock.New(epoch)

	entry := outbox.Entry{
		ID:        "evt-zero-created-at",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"actorId": "actor-1"}),
		// CreatedAt intentionally zero
	}

	svc, buf := newServiceWithLogBuf(t, spec, cap, p, fc)
	result := svc.HandleEvent(context.Background(), entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"HandleEvent must still succeed when CreatedAt is zero")
	require.Len(t, cap.appended, 1)

	// Timestamp must fall back to clk.Now()
	gotTS := cap.appended[0].Timestamp
	if !gotTS.Equal(epoch) {
		t.Errorf("fallback Timestamp: got %v, want clk.Now()=%v", gotTS, epoch)
	}

	// F-02: must emit a Warn-level log when falling back
	logOutput := buf.String()
	if !strings.Contains(logOutput, "WARN") && !strings.Contains(logOutput, "warn") {
		t.Errorf("expected Warn-level log for zero CreatedAt fallback; got log output: %s", logOutput)
	}
}
