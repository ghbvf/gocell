//go:build integration

package main

import (
	"testing"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// buildAccessCoreMemOptions returns the explicit option set that replaces the
// removed accesscore.WithInMemoryDefaults() — user/role + session.Store +
// refresh.Store. All four repositories share the same clock to keep time
// semantics consistent across in-memory tests.
func buildAccessCoreMemOptions(tb testing.TB, clk clock.Clock) []accesscore.Option {
	tb.Helper()
	userStore := accessmem.NewStore(clk)
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
		accesscore.WithUserRepository(userStore.UserRepository()),
		accesscore.WithRoleRepository(userStore.RoleRepository()),
		accesscore.WithSessionStore(sessionStore),
		accesscore.WithRefreshStore(refreshStore),
	}
}
