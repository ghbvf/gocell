//go:build integration

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// buildTestAuditProtocol creates a ledger.Protocol for integration tests.
// The HMAC key is sealed inside the protocol; cells never hold the raw key.
// ref: cmd/corebundle/audit_module.go — production composition uses MustNewProtocol.
func buildTestAuditProtocol(t testing.TB, hmacKey []byte) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "audit namespace parse")
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "audit protocol construction")
	return p
}

// buildTestAuditStore creates a ledger.MemStore for integration tests.
func buildTestAuditStore(t testing.TB, p *ledger.Protocol) *ledger.MemStore {
	t.Helper()
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err, "audit mem store construction")
	return store
}

// auditcoreLedgerOpts returns the WithLedgerProtocol + WithLedgerStore options
// for integration tests, replacing the former WithInMemoryDefaults + WithHMACKey pair.
func auditcoreLedgerOpts(t testing.TB, hmacKey []byte) []auditcore.Option {
	t.Helper()
	p := buildTestAuditProtocol(t, hmacKey)
	store := buildTestAuditStore(t, p)
	return []auditcore.Option{
		auditcore.WithLedgerProtocol(p),
		auditcore.WithLedgerStore(store),
	}
}
