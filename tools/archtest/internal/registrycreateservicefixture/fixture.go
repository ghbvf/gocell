//go:build archtest_fixture

// Package registrycreateservicefixture is the RED fixture for
// CONTRACT-REGISTRY-CREATE-CALLER-01.
//
// The production rule resolves references to registrycore's
// ports.Registry.Create via go/types (info.Uses → *types.Func,
// receiver-bound) and asserts each callsite is in
// corecells/registrycore/slices/registrywrite. ports.Registry lives in
// corecells/registrycore/internal/ports — an internal package that
// tools/archtest cannot import — so this fixture declares its OWN stand-in
// Registry interface mirroring the shape of ports.Registry: a Create method
// with the same structural role. The RedFixture test runs the SAME detector
// core (isCreateCallOnInterface) targeted at THIS package's interface type
// to prove the receiver-binding fires on a genuine interface method call. A
// 0 result means the detector regressed and a direct ports.Registry.Create
// bypass could slip in unnoticed.
//
// (Build-tag gated behind archtest_fixture so it stays out of normal /
// Production builds and is loaded only by the Fixture() RunScope.)
package registrycreateservicefixture

import "context"

// Registry is a local stand-in mirroring the structural role of
// corecells/registrycore/internal/ports.Registry: it has a Create method that
// the production archtest rule forbids being called from outside the
// registrywrite slice. Parameter types use 'any' to avoid importing the
// framework tenant/registry packages (the detector keys on the receiver
// interface name + method name, not parameter types — this divergence is
// intentional and inert).
type Registry interface {
	Create(ctx context.Context, t any, in any) (any, error)
}

// Violation calls Registry.Create from a package that is NOT the sanctioned
// registrywrite slice — the exact bypass the funnel forbids. The detector
// MUST report this call; 0 found means the scanner is fail-open.
func Violation(store Registry) (any, error) {
	return store.Create(context.Background(), nil, nil)
}
