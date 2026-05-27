//go:build integration

package appender_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
)

// errEmitFail is the sentinel returned by failingEmitter.Emit.
var errEmitFail = errors.New("simulated outbox emit failure")

// failingEmitter is a local outbox.Emitter that always returns errEmitFail.
// Used to force RunInTx rollback in atomicity proof tests.
type failingEmitter struct{}

func (failingEmitter) Emit(_ context.Context, _ outbox.Entry) error { return errEmitFail }

// passThroughEmitter accepts all Emit calls with no side effects.
// Used as the "no outbox failure" emitter for idempotency proof.
type passThroughEmitter struct{}

func (passThroughEmitter) Emit(_ context.Context, _ outbox.Entry) error { return nil }

// newIntegProtocol returns a ledger.Protocol for the "auditcore" namespace,
// mirroring newTestLedgerProtocol in adapters/postgres/audit_ledger_store_test.go.
func newIntegProtocol(t *testing.T) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "ledger.NewProtocol for auditcore namespace")
	return p
}

// newValidEntry returns an outbox.Entry with OccurredAt and Principal.ActorID set.
// Caller may adjust ID for replay tests.
func newValidEntry(id string) outbox.Entry {
	return outbox.Entry{
		ID:         id,
		EventType:  "event.user.created.v1",
		Payload:    []byte(`{"actorId":"integ-actor-1","userId":"integ-user-1"}`),
		OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Principal:  outbox.PrincipalMetadata{ActorID: "integ-actor-1"},
	}
}

// countAuditRows returns the number of audit_entries rows for the auditcore namespace.
func countAuditRows(t *testing.T, pool *adapterpg.Pool) int {
	t.Helper()
	var count int
	err := pool.DB().QueryRow(context.Background(),
		"SELECT count(*) FROM audit_entries WHERE namespace = $1", "auditcore").Scan(&count)
	require.NoError(t, err)
	return count
}

// TestL2Atomicity_appender_RollsBack is the hybrid atomicity proof for the
// shared appender.Service used by all four auditappend* slices.
//
// Contract (L2-OUTBOX-ATOMICITY-COVERAGE-01): if outbox.Emit fails inside
// RunInTx, the transaction must roll back — no audit_entries row is persisted.
// Negative control confirms the happy path commits exactly one row.
func TestL2Atomicity_appender_RollsBack(t *testing.T) {
	ctx := context.Background()
	proto := newIntegProtocol(t)
	fc := clockmock.New(storetest.EpochAnchor())
	spec := appender.MustNewSpec("auditappenduser")

	// --- Failure path: Emit fails → RunInTx rolls back → no audit_entries row ---
	failPool := sharedPG.NewPerTestPool(t)
	failTxm := adapterpg.NewTxManager(failPool)
	failStore, err := adapterpg.NewLedgerStore(failPool.DB(), failTxm, proto, fc)
	require.NoError(t, err)

	failSvc, err := appender.NewService(
		spec, failStore, proto, slog.Default(), fc,
		appender.WithEmitter(outbox.WrapEmitterForCell(failingEmitter{})),
		appender.WithTxManager(persistence.WrapForCell(failTxm)),
	)
	require.NoError(t, err)

	countBefore := countAuditRows(t, failPool)
	res := failSvc.HandleEvent(ctx, newValidEntry("atomicity-evt-fail"))
	assert.NotEqual(t, outbox.DispositionAck, res.Disposition,
		"failing emitter must not produce Ack")
	countAfter := countAuditRows(t, failPool)
	assert.Equal(t, countBefore, countAfter,
		"store.Append must roll back atomically with outbox Emit failure: no new row must persist")

	// --- Positive control: pass-through emitter → Ack + exactly 1 row ---
	okPool := sharedPG.NewPerTestPool(t)
	okTxm := adapterpg.NewTxManager(okPool)
	okStore, err := adapterpg.NewLedgerStore(okPool.DB(), okTxm, proto, fc)
	require.NoError(t, err)

	okSvc, err := appender.NewService(
		spec, okStore, proto, slog.Default(), fc,
		appender.WithEmitter(outbox.WrapEmitterForCell(passThroughEmitter{})),
		appender.WithTxManager(persistence.WrapForCell(okTxm)),
	)
	require.NoError(t, err)

	okRes := okSvc.HandleEvent(ctx, newValidEntry("atomicity-evt-ok"))
	assert.Equal(t, outbox.DispositionAck, okRes.Disposition,
		"pass-through emitter must Ack")
	assert.Equal(t, 1, countAuditRows(t, okPool),
		"exactly one audit_entries row must be committed on success")
}

// TestL2Atomicity_appender_ReplayIdempotent is the consumer idempotency proof
// for appender.Service (L2-OUTBOX-ATOMICITY-COVERAGE-01).
//
// Contract: delivering the same outbox.Entry twice (same entry.ID → same
// EventID fingerprint) must Ack both times and produce exactly ONE
// audit_entries row (ErrAuditLedgerAlreadyExists → Ack path).
func TestL2Atomicity_appender_ReplayIdempotent(t *testing.T) {
	ctx := context.Background()
	proto := newIntegProtocol(t)
	fc := clockmock.New(storetest.EpochAnchor())
	spec := appender.MustNewSpec("auditappenduser")

	pool := sharedPG.NewPerTestPool(t)
	txm := adapterpg.NewTxManager(pool)
	store, err := adapterpg.NewLedgerStore(pool.DB(), txm, proto, fc)
	require.NoError(t, err)

	svc, err := appender.NewService(
		spec, store, proto, slog.Default(), fc,
		appender.WithEmitter(outbox.WrapEmitterForCell(passThroughEmitter{})),
		appender.WithTxManager(persistence.WrapForCell(txm)),
	)
	require.NoError(t, err)

	entry := newValidEntry("replay-idempotent-evt-1")

	// First delivery — must Ack and commit one row.
	first := svc.HandleEvent(ctx, entry)
	require.Equal(t, outbox.DispositionAck, first.Disposition,
		"first HandleEvent must Ack")
	assert.Equal(t, 1, countAuditRows(t, pool),
		"first delivery must produce exactly one audit_entries row")

	// Second delivery of identical entry — ErrAuditLedgerAlreadyExists → Ack.
	second := svc.HandleEvent(ctx, entry)
	assert.Equal(t, outbox.DispositionAck, second.Disposition,
		"idempotent replay must Ack, not Requeue/Reject")
	assert.Equal(t, 1, countAuditRows(t, pool),
		"idempotent replay must not add a second audit_entries row")
}
