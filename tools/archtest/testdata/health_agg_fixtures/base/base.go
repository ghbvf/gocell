// Package base provides a fake ManagedResource implementation that fixtures
// embed to test promoted-method detection in HEALTH-AGG-01.
package base

import "context"

// Worker is a local mirror of kernel/worker.Worker. The fixture module is
// isolated (no gocell dependency), so we define the interface inline.
// The archtest only checks that a method named "Worker" exists — the exact
// return type is not relevant to the HEALTH-AGG-01 rule.
type Worker interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// FakeResource is a minimal implementation of lifecycle.ManagedResource used
// by fixture packages to test promoted-method detection. Renamed from
// PGResource after adapters/postgres.PGResource was deleted; the type name
// no longer implies any postgres-specific semantics.
type FakeResource struct{}

func (*FakeResource) Checkers() map[string]func(context.Context) error { return nil }
func (*FakeResource) Worker() Worker                                    { return nil }
func (*FakeResource) Close(_ context.Context) error                     { return nil }
