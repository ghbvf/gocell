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
	chain := domain.NewHashChain(testHMACKey)
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

	chain := domain.NewHashChain(testHMACKey)
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
