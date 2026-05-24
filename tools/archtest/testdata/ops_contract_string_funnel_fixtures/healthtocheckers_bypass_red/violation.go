// Package healthtocheckers_bypass_red is a testdata fixture for
// OPS-CONTRACT-STRING-FUNNEL-01 F1: a package that authors a readiness probe via
// adapterutil.HealthToCheckers but is NOT in readyProbeSanctionedPkgs. Such a
// caller returns the map directly (no composite-lit key for the meta-check's
// key-fold to catch), so it must be flagged by
// nonSanctionedHealthToCheckersViolations. It imports the real adapterutil so
// the type-resolved isHealthToCheckersCall matches; testdata is excluded from
// RunTypedProduction, so this fixture never trips the production meta-check.
package healthtocheckers_bypass_red

import (
	"context"

	"github.com/ghbvf/gocell/adapters/adapterutil"
)

// authorProbe smuggles a probe name past the funnel by calling HealthToCheckers
// from a non-sanctioned package with a bare string literal.
func authorProbe() map[string]func(context.Context) error {
	return adapterutil.HealthToCheckers("bypass_ready", func(context.Context) error { return nil }, 0)
}
