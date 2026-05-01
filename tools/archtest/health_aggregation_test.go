package archtest

// HEALTH-AGG-01: Any exported type in runtime/ or adapters/ that exposes a
// Checkers() or HealthCheckers() method must implement the full
// kernellifecycle.ManagedResource interface (i.e., also have Worker() and
// Close() methods). This prevents the "register health checkers but forget the
// rest of the lifecycle contract" class of bugs that WithRelayHealth
// represented.
//
// Implementation: golang.org/x/tools/go/packages + go/types — types.NewMethodSet
// surfaces promoted methods from embedded fields, so a type that satisfies the
// contract via embedding (e.g. struct embedding *PGResource) is correctly
// recognized as implementing ManagedResource.
//
// Enforcement scope: runtime/, adapters/ packages only.
// Excluded: cells/, kernel/cell/ — HealthCheckersContributor is a different
// interface that intentionally doesn't bundle Worker/Close.

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typeMethodSet collects every exported method (own + promoted) on every
// exported named type loaded from the requested patterns.
type typeMethodSet struct {
	// methods maps "<pkg>.<TypeName>" → set of method names.
	methods map[string]map[string]struct{}
}

func newTypeMethodSet() *typeMethodSet {
	return &typeMethodSet{methods: make(map[string]map[string]struct{})}
}

func (s *typeMethodSet) add(qualified, methodName string) {
	if _, ok := s.methods[qualified]; !ok {
		s.methods[qualified] = make(map[string]struct{})
	}
	s.methods[qualified][methodName] = struct{}{}
}

func (s *typeMethodSet) has(qualified, methodName string) bool {
	if ms, ok := s.methods[qualified]; ok {
		_, hit := ms[methodName]
		return hit
	}
	return false
}

// collectMethodSets loads patterns under modRoot with full type info and
// records every exported method (own or promoted) on every exported named type.
// Keys are qualified by import path so types from different packages with the
// same simple name do not collide.
func collectMethodSets(t *testing.T, modRoot string, patterns ...string) *typeMethodSet {
	t.Helper()
	pkgs, errs, err := typeseval.LoadPackages(modRoot, patterns...)
	require.NoError(t, err, "packages.Load")
	require.Empty(t, errs, "package load errors: %v", errs)

	s := newTypeMethodSet()
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			if !ast.IsExported(name) {
				continue
			}
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			qualified := pkg.PkgPath + "." + name
			ms := types.NewMethodSet(types.NewPointer(named))
			for sel := range ms.Methods() {
				if sel.Obj().Exported() {
					s.add(qualified, sel.Obj().Name())
				}
			}
		}
	}
	return s
}

// isManagedResource returns true when qualified type carries the full
// ManagedResource trio (Checkers + Worker + Close).
func isManagedResource(s *typeMethodSet, qualified string) bool {
	return s.has(qualified, "Checkers") &&
		s.has(qualified, "Worker") &&
		s.has(qualified, "Close")
}

// exposesHealthCheckerMethod returns true when qualified type advertises
// health-checking via Checkers() or the legacy HealthCheckers() spelling.
//
// Note: "Health(ctx)" (e.g. adapters/postgres.Pool.Health) is intentionally
// NOT included. Pool.Health is a connectivity probe with different semantics;
// Pool is wrapped by adapters/postgres.PGResource which carries the full
// ManagedResource contract. Adding "Health" here would incorrectly flag Pool.
func exposesHealthCheckerMethod(s *typeMethodSet, qualified string) bool {
	return s.has(qualified, "Checkers") || s.has(qualified, "HealthCheckers")
}

// TestHealthCheckersImpliesManagedResource (HEALTH-AGG-01) asserts that every
// exported type in runtime/ or adapters/ that exposes Checkers() or
// HealthCheckers() also implements the full ManagedResource contract
// (Checkers + Worker + Close), counting promoted methods from embedded fields.
func TestHealthCheckersImpliesManagedResource(t *testing.T) {
	root := findModuleRoot(t)
	s := collectMethodSets(t, root, "./runtime/...", "./adapters/...")

	var violations []string
	for qualified := range s.methods {
		if !exposesHealthCheckerMethod(s, qualified) {
			continue
		}
		if isManagedResource(s, qualified) {
			continue
		}
		var missing []string
		if !s.has(qualified, "Worker") {
			missing = append(missing, "Worker()")
		}
		if !s.has(qualified, "Close") {
			missing = append(missing, "Close()")
		}
		if s.has(qualified, "HealthCheckers") && !s.has(qualified, "Checkers") {
			missing = append(missing, "Checkers() [rename from HealthCheckers]")
		}
		violations = append(violations,
			qualified+" exposes health checker methods but is missing: "+
				strings.Join(missing, ", ")+" (HEALTH-AGG-01: must implement ManagedResource)")
	}

	assert.Empty(t, violations,
		"HEALTH-AGG-01 violation: types exposing health checker methods must implement kernellifecycle.ManagedResource")
}

// TestHealthAggregation_FixtureRegression exercises the fixture set under
// testdata/health_agg_fixtures/ to prove that promoted methods are detected
// (promoted_ok.App must NOT be flagged) and that bare Checkers() declarations
// are still flagged (checkers_only.Bad must be flagged).
func TestHealthAggregation_FixtureRegression(t *testing.T) {
	fixturesRoot := filepath.Join(findArchTestDir(t), "testdata", "health_agg_fixtures")
	s := collectMethodSets(t, fixturesRoot, "./promoted_ok", "./checkers_only", "./base")

	const promotedOkApp = "healthaggfixtures/promoted_ok.App"
	require.True(t, exposesHealthCheckerMethod(s, promotedOkApp),
		"App should expose Checkers() via promoted method from embedded *PGResource")
	assert.True(t, isManagedResource(s, promotedOkApp),
		"App should be ManagedResource via promoted Worker/Close (proves go/types upgrade)")

	const checkersOnlyBad = "healthaggfixtures/checkers_only.Bad"
	require.True(t, exposesHealthCheckerMethod(s, checkersOnlyBad))
	assert.False(t, isManagedResource(s, checkersOnlyBad),
		"Bad declares only Checkers() — must remain flagged as missing Worker/Close")
}
