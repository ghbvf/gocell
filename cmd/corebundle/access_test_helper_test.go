//go:build integration

package main

import (
	"testing"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// testTenantID is the canonical test tenant UUID used across all corebundle
// integration tests. Matches cells/accesscore/cell_test.go:testTenantID and
// tests/integration/l2atomicity canonical tenant.
const testTenantID = "00000000-0000-0000-0000-000000000001"

// buildAccessCoreMemOptions returns the explicit option set that replaces the
// removed accesscore.WithInMemoryDefaults(). WithMemBundle wires the
// (UserRepository, RoleRepository, SetupLock, store-paired TxRunner)
// quadruple from a single backing mem.Store, guaranteeing the cross-repo
// effective-admin invariant and serializing concurrent first-admin
// provisioning via store.mu (PR #595 fix — previously this helper omitted
// WithTxManager and integration tests silently fell back to
// outbox.DemoCellTxManager after Provisioner.mu was deleted).
//
// PG-mode integration tests use accesspg.NewBundle directly and do not
// call this helper.
func buildAccessCoreMemOptions(tb testing.TB, clk clock.Clock) []accesscore.Option {
	tb.Helper()
	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		tb.Fatalf("buildAccessCoreMemOptions: session.NewProtocol: %v", err)
	}
	sessionStore, err := session.NewMemStore(sessionProto, clk)
	if err != nil {
		tb.Fatalf("buildAccessCoreMemOptions: session.NewMemStore: %v", err)
	}
	refreshStore, err := refreshmem.New(accesscore.DefaultRefreshPolicy(), clk, nil)
	if err != nil {
		tb.Fatalf("buildAccessCoreMemOptions: refreshmem.New: %v", err)
	}
	return []accesscore.Option{
		accesscore.WithMemBundle(accessmem.NewBundle(clk)),
		accesscore.WithSessionStore(sessionStore),
		accesscore.WithRefreshStore(refreshStore),
	}
}

// mustNewCASProtocol constructs a CAS Protocol with the given version field.
// This documents the invariant that cells never call cas.NewProtocol directly
// (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest guards that cells never call it
// directly); integration tests are in cmd/ and are explicitly allowed to do so.
func mustNewCASProtocol(t testing.TB, versionField string) *cas.Protocol {
	t.Helper()
	p, err := cas.NewProtocol(cas.WithVersionField(versionField))
	if err != nil {
		t.Fatalf("cas.NewProtocol: %v", err)
	}
	return p
}
