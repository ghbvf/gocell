//go:build integration

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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

// auditcoreLedgerPGOpts returns the WithLedgerProtocol + WithLedgerStore options
// backed by a real PostgreSQL pool. Replaces the in-memory auditcoreLedgerOpts
// for durable-mode test assemblies.
//
// The returned options do NOT include WithTxManager or WithOutboxDeps — callers
// are responsible for wiring those (same pattern as audit_module.go durable path).
func auditcoreLedgerPGOpts(t testing.TB, pool *adapterpg.Pool, txMgr *adapterpg.TxManager, hmacKey []byte) []auditcore.Option {
	t.Helper()
	p := buildTestAuditProtocol(t, hmacKey)
	pgStore, err := adapterpg.NewLedgerStore(pool.DB(), txMgr, p, clock.Real())
	require.NoError(t, err, "auditcoreLedgerPGOpts: NewLedgerStore")
	return []auditcore.Option{
		auditcore.WithLedgerProtocol(p),
		auditcore.WithLedgerStore(pgStore),
		auditcore.WithTxManager(persistence.WrapForCell(txMgr)),
		auditcore.WithOutboxDeps(nil, outbox.WrapWriterForCell(adapterpg.NewOutboxWriter(clock.Real()))),
	}
}
