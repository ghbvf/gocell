package auditverify

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestService(t testing.TB) (*Service, *mem.AuditRepository) {
	t.Helper()
	repo := mem.NewAuditRepository()
	svc, err := NewService(repo, testHMACKey, slog.Default(), WithTxManager(&stubTxRunner{}))
	require.NoError(t, err)
	return svc, repo
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	repo := mem.NewAuditRepository()
	_, err := NewService(repo, testHMACKey, slog.Default() /* no WithTxManager */)
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
	_, err := NewService(repo, shortKey, slog.Default(), WithTxManager(&stubTxRunner{}))
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "domain *errcode.Error must pass through unchanged")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "audit hmac key too short")
	// The slice constructor must not double-wrap the message — cell.initSlices
	// owns the "auditverify: %w" prefix.
	assert.NotContains(t, err.Error(), "auditverify: auditverify:",
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

func TestService_VerifyChain_Empty(t *testing.T) {
	svc, _ := newTestService(t)
	result, err := svc.VerifyChain(context.Background(), 0, 100)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 0, result.EntriesChecked)
}

func TestService_VerifyChain_ValidEntries(t *testing.T) {
	svc, repo := newTestService(t)

	// Build a valid chain using the same HMAC key.
	chain, err := domain.NewHashChain(testHMACKey)
	require.NoError(t, err)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"), time.Now())
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 3, result.EntriesChecked)
}

func TestService_VerifyChain_TamperedEntry(t *testing.T) {
	svc, repo := newTestService(t)

	chain, err := domain.NewHashChain(testHMACKey)
	require.NoError(t, err)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"), time.Now())
		if i == 1 {
			// Tamper with the second entry.
			entry.Hash = "tampered-hash"
		}
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, 1, result.FirstInvalidIndex)
}
