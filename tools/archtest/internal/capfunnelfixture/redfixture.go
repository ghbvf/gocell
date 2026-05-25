//go:build archtest_fixture

// Package capfunnelfixture contains intentionally-violating call sites against
// the shared-infrastructure adapter constructors banned by
// CAPABILITY-PROVIDER-FUNNEL-01 (see capability_provider_funnel_test.go).
//
// Gated by the archtest_fixture build tag; production builds never see this
// file. Loaded by TestCapabilityProviderFunnel_RedFixtureDetected via
// archtest.RunTypedFixture (which injects the archtest_fixture tag).
//
// # Forms covered
//
// The banned calls appear across the three callee shapes archtest.ResolvePackageRef
// resolves, and across both adapter packages:
//
//   - qualified-import (`adapterpg.NewPool(...)`, `adapterredis.NewClient(...)`)
//   - alias-import     (`pgalias.NewPool(...)`)
//   - dot-import       (`NewTxManager(...)` after `import . "…/postgres"`, see dotimport.go)
package capfunnelfixture

import (
	"context"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	pgalias "github.com/ghbvf/gocell/adapters/postgres"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
)

// qualifiedPGCalls exercises the three banned postgres constructors in
// qualified-import form (3 hits).
func qualifiedPGCalls(ctx context.Context, clk clock.Clock) {
	_, _ = adapterpg.NewPool(ctx, adapterpg.Config{})
	var p *adapterpg.Pool
	_ = adapterpg.NewTxManager(p)
	_ = adapterpg.NewOutboxWriter(clk)
}

// qualifiedRedisCall exercises the banned redis client constructor in
// qualified-import form (1 hit).
func qualifiedRedisCall(ctx context.Context) {
	_, _ = adapterredis.NewClient(ctx, adapterredis.Config{})
}

// aliasedCall exercises alias-import resolution (1 hit).
func aliasedCall(ctx context.Context) {
	_, _ = pgalias.NewPool(ctx, pgalias.Config{})
}
