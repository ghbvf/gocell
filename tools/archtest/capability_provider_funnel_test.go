// INVARIANT: CAPABILITY-PROVIDER-FUNNEL-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckCapabilityProviderFunnel + scanCapabilityProviderViolations +
// shortPkg + the full package godoc, all module-path-agnostic via
// PlatformModulePath) lives in the non-test companion capability_provider_funnel.go
// (M3 #1639) so it is fork-safe — single source, no parallel rule body.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCapabilityProviderFunnel_CompositionRootOnly dogfoods
// CAPABILITY-PROVIDER-FUNNEL-01 against GoCell itself by calling the same
// CheckCapabilityProviderFunnel that holds the importable rule body — single
// source. The shared-infra adapter constructors may only be called from
// cmd/corebundle/cap_wiring.go; cell module files must consume the injected
// capability.PGProvider / capability.RedisProvider.
func TestCapabilityProviderFunnel_CompositionRootOnly(t *testing.T) {
	Report(t, capFunnelRuleID, CheckCapabilityProviderFunnel(t, ConfigForExternalCell{}))
}

// TestCapabilityProviderFunnel_RedFixtureDetected asserts the production rule
// catches every banned call shape (qualified / aliased / dot) across both
// adapter packages in the RED fixture, gated by `//go:build archtest_fixture`.
// It runs the same scanCapabilityProviderViolations the production Check uses
// (single source) against the synthetic fixture packages.
//
// Coverage: 3 PG qualified (NewPool/NewTxManager/NewOutboxWriter) + 1 redis
// qualified (NewClient) + 1 aliased (NewPool) + 1 dot-import (NewTxManager) = 6.
func TestCapabilityProviderFunnel_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/capfunnelfixture/..."},
	),

		scanCapabilityProviderViolations)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	// Equality (not ≥) so the fixture cannot drift silently — any change to the
	// fixture files must update this count.
	assert.Len(t, diags, 6,
		"fixture must yield exactly 6 CAPABILITY-PROVIDER-FUNNEL-01 hits "+
			"(3 PG qualified + 1 redis qualified + 1 aliased + 1 dot-import); "+
			"if the fixture changes intentionally, update the expected count")
}
