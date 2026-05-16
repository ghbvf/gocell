package archtest

// INVARIANT: HEALTH-AGG-01
//
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
// Excluded: cells/, kernel/cell/ — health probes are registered via
// Registry.Health(...) and do not bundle Worker/Close.

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
// NOT included. Pool (adapters/postgres.Pool) now directly implements
// ManagedResource; Pool.Health is a connectivity probe with different semantics
// from Checkers(). Adding "Health" here would incorrectly flag Pool.
func exposesHealthCheckerMethod(s *typeMethodSet, qualified string) bool {
	return s.has(qualified, "Checkers") || s.has(qualified, "HealthCheckers")
}

// TestHealthCheckersImpliesManagedResource (HEALTH-AGG-01) asserts that every
// exported type in runtime/ or adapters/ that exposes Checkers() or
// HealthCheckers() also implements the full ManagedResource contract
// (Checkers + Worker + Close), counting promoted methods from embedded fields.
func TestHealthCheckersImpliesManagedResource(t *testing.T) {
	s := newTypeMethodSet()
	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/...", "./adapters/..."},
		func(p *Pass) []Diagnostic {
			accumulateMethodSet(p, s)
			return nil
		})

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
	s := newTypeMethodSet()
	RunTypedDir(t, fixturesRoot, TypedOpts{Tests: false}, []string{"./promoted_ok", "./checkers_only", "./base"},
		func(p *Pass) []Diagnostic {
			accumulateMethodSet(p, s)
			return nil
		})

	const promotedOkApp = "healthaggfixtures/promoted_ok.App"
	require.True(t, exposesHealthCheckerMethod(s, promotedOkApp),
		"App should expose Checkers() via promoted method from embedded *FakeResource")
	assert.True(t, isManagedResource(s, promotedOkApp),
		"App should be ManagedResource via promoted Worker/Close (proves go/types upgrade)")

	const checkersOnlyBad = "healthaggfixtures/checkers_only.Bad"
	require.True(t, exposesHealthCheckerMethod(s, checkersOnlyBad))
	assert.False(t, isManagedResource(s, checkersOnlyBad),
		"Bad declares only Checkers() — must remain flagged as missing Worker/Close")
}

var adapterReadyProbeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*_ready$`)

// adapterCheckerNameViolationsFromPass scans all files in a Pass for Checkers()
// methods on exported receiver types and validates each probe name.
func adapterCheckerNameViolationsFromPass(fset *token.FileSet, files []*ast.File, info *types.Info, rel string) []string {
	var violations []string
	for _, file := range files {
		scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Name.Name != "Checkers" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				return
			}
			recv := scanner.ReceiverTypeName(fn.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				return
			}
			for _, name := range checkerNamesFromFuncPass(fset, info, fn) {
				if !adapterReadyProbeNamePattern.MatchString(name) {
					violations = append(violations, rel+"."+recv+" Checkers probe "+strconv.Quote(name)+" must be snake_case and end with _ready")
				}
			}
		})
	}
	return violations
}

func checkerNamesFromFuncPass(_ *token.FileSet, info *types.Info, fn *ast.FuncDecl) []string {
	var names []string
	scanner.EachInSubtree[ast.KeyValueExpr](fn.Body, func(kv *ast.KeyValueExpr) {
		tv, ok := info.Types[kv.Key]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			return
		}
		names = append(names, constant.StringVal(tv.Value))
	})
	scanner.EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HealthToCheckers" || len(call.Args) == 0 {
			return
		}
		obj, ok := info.Uses[sel.Sel].(*types.Func)
		if !ok || !strings.HasSuffix(obj.Pkg().Path(), "adapters/adapterutil") {
			return
		}
		name, ok := constStringValue(info, call.Args[0])
		if !ok {
			return
		}
		names = append(names, name)
	})
	return names
}

// healthCheckerCallNameViolationsFromPass scans all files in a Pass for
// WithHealthChecker call sites and validates each probe name.
func healthCheckerCallNameViolationsFromPass(_ *token.FileSet, files []*ast.File, info *types.Info, rel string) []string {
	var violations []string
	for _, file := range files {
		scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if selectorName(call.Fun) != "WithHealthChecker" || len(call.Args) == 0 {
				return
			}
			name, ok := constStringValue(info, call.Args[0])
			if !ok {
				return
			}
			if !adapterReadyProbeNamePattern.MatchString(name) {
				violations = append(violations, rel+" bootstrap.WithHealthChecker probe "+strconv.Quote(name)+" must be snake_case and end with _ready")
			}
		})
	}
	return violations
}

func constStringValue(info *types.Info, expr ast.Expr) (string, bool) {
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}
