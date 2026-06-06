// Package green_compliant is a GREEN fixture for SCAFFOLD-DERIVED-FORCEOVERWRITE-01:
// it imports pathsafe and uses the sanctioned PlanSet constructor but never
// references DerivedOverwrite — 0 violations expected (empty golden). This
// proves the rule is specific to DerivedOverwrite, not to any pathsafe use.
package green_compliant

import "github.com/ghbvf/gocell/pkg/pathsafe"

// build constructs a plan set via the public, non-force-overwrite constructor.
func build() (pathsafe.PlanSet, error) {
	return pathsafe.NewPlanSet(nil)
}
