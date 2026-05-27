package archtest

// INVARIANT: HEALTH-AGG-01
//
// HEALTH-AGG-01: Any exported type in runtime/ or adapters/ that exposes a
// Probes() method must implement the full kernellifecycle.ManagedResource
// interface (i.e., also have Worker() and Close() methods). This prevents the
// "register health probes but forget the rest of the lifecycle contract" class
// of bugs that WithRelayHealth represented.
//
// Implementation: golang.org/x/tools/go/packages + go/types — types.NewMethodSet
// surfaces promoted methods from embedded fields, so a type that satisfies the
// contract via embedding (e.g. struct embedding *FakeResource) is correctly
// recognized as implementing ManagedResource.
//
// Enforcement scope: runtime/, adapters/ packages only.
// Excluded: cells/, kernel/cell/ — health probes are registered via
// Registry.RegisterReadiness(...) and do not bundle Worker/Close.

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

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

// accumulateMethodSet accumulates every exported method (own or promoted) on
// every exported named type in p.Pkg into s. Keys are qualified by import path
// so types from different packages with the same simple name do not collide.
// Called once per Pass inside RunTyped/RunTypedDir.
func accumulateMethodSet(p *Pass, s *typeMethodSet) {
	if p.Pkg == nil {
		return
	}
	scope := p.Pkg.Scope()
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
		qualified := p.Pkg.Path() + "." + name
		ms := types.NewMethodSet(types.NewPointer(named))
		for sel := range ms.Methods() {
			if sel.Obj().Exported() {
				s.add(qualified, sel.Obj().Name())
			}
		}
	}
}

// isManagedResource returns true when qualified type carries the full
// ManagedResource trio (Probes + Worker + Close).
func isManagedResource(s *typeMethodSet, qualified string) bool {
	return s.has(qualified, "Probes") &&
		s.has(qualified, "Worker") &&
		s.has(qualified, "Close")
}

// exposesHealthProbeMethod returns true when qualified type advertises
// health-checking via Probes().
//
// Note: "Health(ctx)" (e.g. adapters/postgres.Pool.Health) is intentionally
// NOT included. Pool (adapters/postgres.Pool) now directly implements
// ManagedResource; Pool.Health is a connectivity probe with different semantics
// from Probes(). Adding "Health" here would incorrectly flag Pool.
func exposesHealthProbeMethod(s *typeMethodSet, qualified string) bool {
	return s.has(qualified, "Probes")
}

// healthAggSanctionedAdapterCarveOuts lists exported types in runtime/ or
// adapters/ that intentionally do NOT implement ManagedResource directly even
// though they expose Probes() — their Probes/Worker primitives are consumed by
// a single sanctioned adapter that owns the Close obligation.
//
// Each entry MUST cite the ADR that closes the carve-out semantically. The
// downstream Hard guard for *runtime/outbox.Relay is archtest
// RELAY-NOT-MANAGEDRESOURCE-01 (relay_isolation_test.go) — that test fails the
// moment *Relay re-satisfies ManagedResource, so this allowlist cannot widen
// in the wrong direction without an immediate second archtest failure.
var healthAggSanctionedAdapterCarveOuts = map[string]string{
	// *Relay's Probes/Worker are consumed exclusively by the
	// package-private runtime/bootstrap.relayAdapter (single sanctioned
	// holder) which owns Close → relay.Stop. Re-adding Close to *Relay
	// would regress the type isolation guarded by
	// RELAY-NOT-MANAGEDRESOURCE-01.
	"github.com/ghbvf/gocell/runtime/outbox.Relay": "docs/architecture/202605201400-adr-relay-managedresource-isolation.md",
}

// TestHealthCheckersImpliesManagedResource (HEALTH-AGG-01) asserts that every
// exported type in runtime/ or adapters/ that exposes Probes() also implements
// the full ManagedResource contract (Probes + Worker + Close), counting
// promoted methods from embedded fields.
//
// Exceptions are limited to the sanctioned-adapter carve-out map
// (healthAggSanctionedAdapterCarveOuts) where the Close obligation is owned
// by a package-private adapter and the corresponding downstream Hard guard
// pins the type isolation.
func TestHealthCheckersImpliesManagedResource(t *testing.T) {
	s := newTypeMethodSet()
	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/...", "./adapters/..."},
		func(p *Pass) []Diagnostic {
			accumulateMethodSet(p, s)
			return nil
		})

	var violations []string
	for qualified := range s.methods {
		if !exposesHealthProbeMethod(s, qualified) {
			continue
		}
		if isManagedResource(s, qualified) {
			continue
		}
		if _, exempt := healthAggSanctionedAdapterCarveOuts[qualified]; exempt {
			continue
		}
		var missing []string
		if !s.has(qualified, "Worker") {
			missing = append(missing, "Worker()")
		}
		if !s.has(qualified, "Close") {
			missing = append(missing, "Close()")
		}
		violations = append(violations,
			qualified+" exposes Probes() but is missing: "+
				strings.Join(missing, ", ")+" (HEALTH-AGG-01: must implement ManagedResource)")
	}

	assert.Empty(t, violations,
		"HEALTH-AGG-01 violation: types exposing Probes() must implement kernellifecycle.ManagedResource")
}

// TestHealthCheckersImpliesManagedResource_CarveOutsProbesOnly asserts the
// integrity of the sanctioned-adapter carve-out: every entry must still
// expose Probes() (otherwise the carve-out is dead) and must still NOT
// satisfy the full ManagedResource contract (otherwise the carve-out is
// vacuous / can be deleted). This guards against silent drift in either
// direction without forcing the main HEALTH-AGG-01 assertion to re-scan.
func TestHealthCheckersImpliesManagedResource_CarveOutsProbesOnly(t *testing.T) {
	s := newTypeMethodSet()
	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/...", "./adapters/..."},
		func(p *Pass) []Diagnostic {
			accumulateMethodSet(p, s)
			return nil
		})

	for qualified, adr := range healthAggSanctionedAdapterCarveOuts {
		assert.Truef(t, exposesHealthProbeMethod(s, qualified),
			"HEALTH-AGG-01 carve-out %q (ADR %s) no longer exposes Probes — delete the carve-out entry",
			qualified, adr)
		assert.Falsef(t, isManagedResource(s, qualified),
			"HEALTH-AGG-01 carve-out %q (ADR %s) now fully implements ManagedResource — delete the carve-out entry; "+
				"the type can rejoin the standard rule",
			qualified, adr)
	}
}

// TestHealthAggregation_FixtureRegression exercises the fixture set under
// testdata/health_agg_fixtures/ to prove that promoted methods are detected
// (promoted_ok.App must NOT be flagged) and that bare Probes() declarations
// are still flagged (checkers_only.Bad must be flagged).
func TestHealthAggregation_FixtureRegression(t *testing.T) {
	fixturesRoot := filepath.Join(findArchTestDir(t), "testdata", "health_agg_fixtures")
	s := newTypeMethodSet()
	RunTypedDir(t, fixturesRoot, TypedOpts{Tests: false}, []string{"./promoted_ok", "./checkers_only", "./base"},
		func(p *Pass) []Diagnostic {
			accumulateMethodSet(p, s)
			return nil
		})

	const promotedOkApp = "healthaggfixtures/promoted_ok.App"
	require.True(t, exposesHealthProbeMethod(s, promotedOkApp),
		"App should expose Probes() via promoted method from embedded *FakeResource")
	assert.True(t, isManagedResource(s, promotedOkApp),
		"App should be ManagedResource via promoted Worker/Close (proves go/types upgrade)")

	const checkersOnlyBad = "healthaggfixtures/checkers_only.Bad"
	require.True(t, exposesHealthProbeMethod(s, checkersOnlyBad))
	assert.False(t, isManagedResource(s, checkersOnlyBad),
		"Bad declares only Probes() — must remain flagged as missing Worker/Close")
}

// Adapter/runtime ready-probe NAME validation (snake_case + _ready, single
// source) moved to the Hard funnel PROBENAME-SEALED-FUNNEL-01
// (probename_sealed_funnel_test.go). The Soft regex scanners that lived here
// — adapterCheckerNameViolationsFromPass / checkerNamesFromFuncPass /
// healthCheckerCallNameViolationsFromPass — were removed (no parallel
// Soft+Hard).
