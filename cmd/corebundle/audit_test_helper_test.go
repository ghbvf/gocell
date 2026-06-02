//go:build integration

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// buildTestAuditProtocol creates a ledger.Protocol for integration tests.
// The HMAC key is sealed inside the protocol; cells never hold the raw key.
// ref: cellmodules/auditcore/module.go — production composition uses NewProtocol.
func buildTestAuditProtocol(t testing.TB, hmacKey []byte) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "audit namespace parse")
	p, err := ledger.NewProtocol(
		ns,
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "audit protocol construction")
	return p
}

// buildTestAuditStore creates a ledger.Store (backed by MemStore) for integration tests.
// F16: return type is ledger.Store (interface) so callers are decoupled from the
// concrete MemStore type; only storetest tamper-helpers need the concrete type.
func buildTestAuditStore(t testing.TB, p *ledger.Protocol) ledger.Store {
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

// buildTestBootstrapAuditChain constructs a (raw store, sealed wrapper) pair on
// the bootstrap namespace for integration tests. The sealed wrapper is injected
// into auditcore via WithBootstrapStore so the auditappendbootstrap subscriber
// (consuming event.auth.bootstrap-failed.v1) writes the bootstrap-namespace
// ledger. The raw store is returned alongside so test assertions can call Query
// on it directly, and so a ledger.MultiStore can include it next to the auditcore
// relay store (mirroring the production wiring in cellmodules/auditcore/module.go).
func buildTestBootstrapAuditChain(t testing.TB, hmacKey []byte) (ledger.Store, *audit.BootstrapLedgerStore) {
	t.Helper()
	p, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "bootstrap audit protocol construction")
	raw, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err, "bootstrap audit mem store construction")
	wrapped, err := audit.NewBootstrapLedgerStore(raw)
	require.NoError(t, err, "wrap bootstrap audit ledger store")
	return raw, wrapped
}
