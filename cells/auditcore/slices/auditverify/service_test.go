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
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestService() (*Service, *mem.AuditRepository) {
	repo := mem.NewAuditRepository()
	return NewService(repo, testHMACKey, slog.Default()), repo
}

func TestService_VerifyChain_Empty(t *testing.T) {
	svc, _ := newTestService()
	result, err := svc.VerifyChain(context.Background(), 0, 100)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 0, result.EntriesChecked)
}

func TestService_VerifyChain_ValidEntries(t *testing.T) {
	svc, repo := newTestService()

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
	svc, repo := newTestService()

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
