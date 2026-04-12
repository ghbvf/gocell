package auditverify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: event.audit.integrity-verified.v1 — verify publishes {valid, first_invalid_index, entries_checked}.
func TestEventAuditIntegrityVerifiedV1Publish(t *testing.T) {
	svc, _ := newTestService()

	result, err := svc.VerifyChain(context.Background(), 0, 100)
	require.NoError(t, err)
	assert.True(t, result.Valid, "contract: empty chain should verify as valid")
	assert.Equal(t, 0, result.EntriesChecked)
}
