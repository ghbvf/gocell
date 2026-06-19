package postgres

// Unit tests for NewAuditChainVerifyStore's constructor-derived namespace→protocol
// mapping (#1755 F1). These need no live PG: the variadic protocol validation runs
// BEFORE the admin-pool AuditAdminReadyCheck preflight, so a non-nil zero-value
// *Pool reaches (and trips) the validation without ever touching the DB. The
// live-PG enumerate/verify/tamper coverage lives in the integration-tagged file.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// unitVerifyProtocol builds a real HMAC Protocol for namespace ns without any PG
// dependency (mirrors the integration file's newVerifyProtocol, inlined here so
// this non-tagged unit file does not depend on integration-tagged helpers).
func unitVerifyProtocol(t *testing.T, ns string, seed byte) *ledger.Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i+1) ^ seed
	}
	nsid, err := ledger.ParseNamespaceID(ns)
	require.NoError(t, err, "ParseNamespaceID(%q)", ns)
	p, err := ledger.NewProtocol(nsid, key,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}))
	require.NoError(t, err, "NewProtocol(%q)", ns)
	return p
}

// TestNewAuditChainVerifyStore_DuplicateNamespace is the F1 fail-closed guard: with
// the constructor deriving the namespace key from each protocol's own Namespace(),
// two protocols sharing a namespace are an ambiguous registration and must be
// rejected — there is no caller-supplied key to silently overwrite. The duplicate
// check runs before the admin-pool preflight, so a zero-value *Pool suffices.
func TestNewAuditChainVerifyStore_DuplicateNamespace(t *testing.T) {
	t.Parallel()
	a := unitVerifyProtocol(t, "auditcore", 0x00)
	b := unitVerifyProtocol(t, "auditcore", 0xFF) // same namespace, different key
	_, err := NewAuditChainVerifyStore(context.Background(), &Pool{}, a, b)
	require.Error(t, err, "duplicate namespace must fail closed")
	var coded *errcode.Error
	assert.True(t, errors.As(err, &coded), "expected *errcode.Error, got %T", err)
}

// TestNewAuditChainVerifyStore_NoProtocols rejects an empty protocol set (the
// store would have nothing to verify any chain with).
func TestNewAuditChainVerifyStore_NoProtocols(t *testing.T) {
	t.Parallel()
	_, err := NewAuditChainVerifyStore(context.Background(), &Pool{})
	require.Error(t, err, "no protocols must fail closed")
}

// TestNewAuditChainVerifyStore_NilProtocol rejects a nil protocol in the variadic
// set before any pool I/O.
func TestNewAuditChainVerifyStore_NilProtocol(t *testing.T) {
	t.Parallel()
	a := unitVerifyProtocol(t, "auditcore", 0x00)
	_, err := NewAuditChainVerifyStore(context.Background(), &Pool{}, a, nil)
	require.Error(t, err, "nil protocol must fail closed")
}
