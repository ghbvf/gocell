package appender_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	defaultOpts := []appender.Option{appender.WithTxManager(directRunner{})}
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
	assert.Contains(t, ec.Message, "auditappenduser")
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
