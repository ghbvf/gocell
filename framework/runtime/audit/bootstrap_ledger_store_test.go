package audit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// TestNewBootstrapLedgerStore_RejectsNilInner asserts the construction guard.
// The typed wrapper is the Hard upstream defense — passing a nil inner store
// to AppendBootstrapAuthFail is meant to fail at wiring time (cellmodules/auditcore
// injects the store into the auditappendbootstrap slice before bootstrap), never
// at the first 401/429 event.
func TestNewBootstrapLedgerStore_RejectsNilInner(t *testing.T) {
	t.Parallel()
	_, err := audit.NewBootstrapLedgerStore(nil)
	require.Error(t, err)
	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestNewBootstrapLedgerStore_RejectsNonBootstrapNamespace asserts the
// upstream namespace guard. A relay-chain store must not be sealable as the
// bootstrap chain handle.
func TestNewBootstrapLedgerStore_RejectsNonBootstrapNamespace(t *testing.T) {
	t.Parallel()
	relayNS, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		relayNS,
		[]byte("bootstrap-test-hmac-32-bytes-ok!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	mem, err := ledger.NewMemStore(p, clockmock.New(testNow))
	require.NoError(t, err)

	_, err = audit.NewBootstrapLedgerStore(mem)
	require.Error(t, err)
	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
	assert.Contains(t, coded.Message, "bootstrap namespace")
}

// TestBootstrapLedgerStore_DelegatesAppend wraps a MemStore on the bootstrap
// namespace and confirms Append flows through to the inner store.
func TestBootstrapLedgerStore_DelegatesAppend(t *testing.T) {
	t.Parallel()
	p, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		[]byte("bootstrap-test-hmac-32-bytes-ok!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	mem, err := ledger.NewMemStore(p, clockmock.New(testNow))
	require.NoError(t, err)

	wrapped, err := audit.NewBootstrapLedgerStore(mem)
	require.NoError(t, err)
	require.NotNil(t, wrapped)

	entry := &ledger.Entry{
		EventID:   "evt-1",
		EventType: "bootstrap.auth.fail",
		ActorID:   "system:bootstrap",
		Timestamp: testNow,
		Payload:   []byte(`{"reason":"wrong_credentials"}`),
	}
	require.NoError(t, wrapped.Append(context.Background(), entry))

	tail, err := wrapped.Tail(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), tail.SeqNo, "tail seq must reflect the appended entry")
	assert.Equal(t, int64(1), tail.EntryCount)

	valid, firstInvalid, err := wrapped.Verify(context.Background(), 1, tail.SeqNo)
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, int64(-1), firstInvalid)
}

// TestVerifyBootstrapTailOnStartup_EmptyChain asserts the empty-chain path
// returns nil — there's nothing to verify on a fresh store, but the helper
// must not block startup.
func TestVerifyBootstrapTailOnStartup_EmptyChain(t *testing.T) {
	t.Parallel()
	p, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		[]byte("bootstrap-test-hmac-32-bytes-ok!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	mem, err := ledger.NewMemStore(p, clockmock.New(testNow))
	require.NoError(t, err)
	wrapped, err := audit.NewBootstrapLedgerStore(mem)
	require.NoError(t, err)

	require.NoError(t, audit.VerifyBootstrapTailOnStartup(context.Background(), wrapped, nil))
}

// TestVerifyBootstrapTailOnStartup_RejectsNilStore asserts the nil guard.
func TestVerifyBootstrapTailOnStartup_RejectsNilStore(t *testing.T) {
	t.Parallel()
	err := audit.VerifyBootstrapTailOnStartup(context.Background(), nil, nil)
	require.Error(t, err)
	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code)
}

// TestBootstrapLedgerStore_RepoReady_Conformance enrolls *BootstrapLedgerStore
// in the cross-cell repo-readiness conformance suite (CELL-REPO-READYZ-PROBE-01).
// The wrapper delegates RepoReady to the inner Store; this binds that contract
// — corebundle and ssobff wire the wrapper as a healthz.RepoProber even though
// the relay-chain probe currently shares the audit_entries table.
func TestBootstrapLedgerStore_RepoReady_Conformance(t *testing.T) {
	t.Parallel()
	p, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		[]byte("bootstrap-test-hmac-32-bytes-ok!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	mem, err := ledger.NewMemStore(p, clockmock.New(testNow))
	require.NoError(t, err)
	wrapped, err := audit.NewBootstrapLedgerStore(mem)
	require.NoError(t, err)
	celltest.RunRepoReadinessConformance(t, "bootstrap-ledger", wrapped, nil)
}
