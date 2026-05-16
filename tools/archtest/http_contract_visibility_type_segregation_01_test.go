// INVARIANT: HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01
//
// HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01 — no single concrete Go type
// may simultaneously implement a "public-path HTTP contract generated Service
// interface" AND an "internal-path HTTP contract generated Service interface".
//
// Motivation: a type that crosses the public/internal trust boundary at the
// Go type level is a structural trust-boundary violation.  The slice.yaml
// metadata split enforced by FMT-33 (SLICE-HTTP-VISIBILITY-SEGREGATION-01) is
// a Soft guard — it lives in YAML and the Go type system is unaffected.
// This archtest adds the Medium type-aware layer: if a concrete type in
// production code implements both a public-path contract Service interface
// (generated/contracts/http/ without "internalapi" in the path) AND an
// internal-path contract Service interface (generated/contracts/http/…
// containing "internalapi"), the test fails.
//
// Reference implementation: configread pattern —
//
//	cells/configcore/slices/configread/handler.go GetAdapter / ListAdapter
//	cells/configcore/slices/configreadinternal/handler.go InternalGetAdapter
//
// After the F1' refactor, devicecell follows the same pattern:
//
//	slices/devicecommand/handler.go  EnqueueAdapter/DequeueAdapter/… (public only)
//	slices/devicecommandinternal/handler.go InternalListAdapter (internal only)
//
// The former monolithic internal/devicecmd.Service (which had interface assertion
// blocks for all 6 contracts) is the canonical RED fixture that this rule catches.
//
// AI-rebust: Medium (type-aware via go/types.Implements, cross-package interface
// resolution via typeseval.SharedResolver; the type system itself is the primary
// defense — once Service stops implementing the contract interfaces the violation
// is inexpressible at the compiler level too).
//
// Blindspot inventory (tools: RunTyped + types.Implements; rule scope:
// production non-generated non-test struct types):
//
//   - Adapter pattern bypass: if a single struct T simultaneously holds a public
//     Adapter and an internal Adapter (both embedded), T would implement both
//     categories. In practice the slice-level Adapter types never hold both; they
//     are thin wrappers over the domain Service and each implement exactly one
//     generated interface. This is the residual escape and the rationale for also
//     keeping FMT-33 as a complementary Soft guard.
//
//   - Cross-package alias: if package A declares `type MyService = pkgB.Service`
//     and pkgB.Service implements both categories, MyService would not appear as
//     a named type in the scanner (alias transparent to go/types lookup via
//     types.Unalias). The canonical form (struct type implementing interfaces) is
//     detected correctly.
//
//   - Generic types: a generic struct constrained to implement both public and
//     internal Service interfaces would require explicit type-parameter bounds on
//     both. GoCell does not use such patterns; the scanner does not exercise this
//     shape.
//
// Self-check: TestHTTPContractVisibilityTypeSegregation01_ScannerCatchesViolation
// uses a build-tag-gated fixture that re-implements the old monolithic
// Service shape with all 6 interface assertions (see
// tools/archtest/testdata/http_contract_visibility_type_segregation/fixture.go),
// confirming the rule fires RED on that shape and stays GREEN on the refactored
// repo.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"sync"
	"testing"
)

// contractServiceIfaceSet holds the two visibility buckets of Service interfaces
// from generated HTTP contract packages.
type contractServiceIfaceSet struct {
	mu sync.Mutex
	// public maps "<pkgPath>.Service" → *types.Interface for public-path contracts.
	// Public-path: generated/contracts/http/ packages WITHOUT "internalapi" in path.
	public map[string]*types.Interface
	// internal maps "<pkgPath>.Service" → *types.Interface for internal-path contracts.
	// Internal-path: generated/contracts/http/ packages WITH "internalapi" in path.
	internal map[string]*types.Interface
}

func newContractServiceIfaceSet() *contractServiceIfaceSet {
	return &contractServiceIfaceSet{
		public:   make(map[string]*types.Interface),
		internal: make(map[string]*types.Interface),
	}
}

// isGeneratedHTTPContractPkg reports whether pkgPath is a generated HTTP
// contract package (under generated/contracts/http/).
func isGeneratedHTTPContractPkg(pkgPath string) bool {
	return strings.Contains(pkgPath, "/generated/contracts/http/")
}

// isInternalHTTPContractPkg reports whether a generated HTTP contract package
// is internal-path (its path contains "internalapi").
func isInternalHTTPContractPkg(pkgPath string) bool {
	return strings.Contains(pkgPath, "internalapi")
}

// collectIfacesFromPass extracts the "Service" interface from a Pass that
// corresponds to a generated HTTP contract package and adds it to set.
// No-ops for non-contract packages.
func collectIfacesFromPass(pass *Pass, set *contractServiceIfaceSet) {
	if pass.Pkg == nil {
		return
	}
	pkgPath := pass.Pkg.Path()
	if !isGeneratedHTTPContractPkg(pkgPath) {
		return
	}
	obj := pass.Pkg.Scope().Lookup("Service")
	if obj == nil {
		return
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	if isInternalHTTPContractPkg(pkgPath) {
		set.internal[pkgPath+".Service"] = iface
	} else {
		set.public[pkgPath+".Service"] = iface
	}
}

// httpVisibilityViolation records a concrete type that spans both visibility
// buckets.
type httpVisibilityViolation struct {
	// Rel is the module-relative slash path of the file declaring the type.
	Rel string
	// Line is the source line of the type declaration.
	Line int
	// TypeName is the fully-qualified "<pkgPath>.<Name>" of the violating type.
	TypeName string
	// PublicContracts lists the public-path contract package paths it implements.
	PublicContracts []string
	// InternalContracts lists the internal-path contract package paths it implements.
	InternalContracts []string
}

// checkPassForVisibilityViolations scans all struct types in pass, checks them
// against ifaceSet, and returns a violation for each type that spans both
// public and internal contract buckets.
//
// Blindspot annotation (SCANNER-FRAMEWORK-USAGE-01): the AST walk uses
// EachInSubtree[ast.TypeSpec] to enumerate type declarations. This covers
// all nested struct declarations within a file including those inside
// function bodies (unusual but not illegal Go). The scanner cannot detect
// violations expressed via anonymous struct fields that alias another
// contract-implementing type — this shape does not occur in GoCell.
func checkPassForVisibilityViolations(pass *Pass, ifaceSet *contractServiceIfaceSet) []httpVisibilityViolation {
	if !pass.Typed() {
		return nil
	}
	pkgPath := pass.Pkg.Path()
	// Skip generated/ packages — only check production code.
	if strings.Contains(pkgPath, "/generated/") {
		return nil
	}
	// Skip test-only packages (pkg name ends with _test).
	if strings.HasSuffix(pass.Pkg.Name(), "_test") {
		return nil
	}

	ifaceSet.mu.Lock()
	// Snapshot the interface sets so we don't hold the lock during Implements calls.
	pub := make(map[string]*types.Interface, len(ifaceSet.public))
	for k, v := range ifaceSet.public {
		pub[k] = v
	}
	intl := make(map[string]*types.Interface, len(ifaceSet.internal))
	for k, v := range ifaceSet.internal {
		intl[k] = v
	}
	ifaceSet.mu.Unlock()

	if len(pub) == 0 || len(intl) == 0 {
		return nil
	}

	var out []httpVisibilityViolation
	for _, file := range pass.Files {
		rel := pass.Rel(file)
		// Skip test files and generated files.
		if strings.HasSuffix(rel, "_test.go") || pass.IsGenerated(file) {
			continue
		}

		// Walk all TypeSpec nodes in the file tree.
		EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
			// Only concrete struct types.
			if _, ok := ts.Type.(*ast.StructType); !ok {
				return
			}
			obj := pass.Pkg.Scope().Lookup(ts.Name.Name)
			if obj == nil {
				return
			}
			named, ok := types.Unalias(obj.Type()).(*types.Named)
			if !ok {
				return
			}
			ptrNamed := types.NewPointer(named)

			// Collect which contract categories this type satisfies.
			var pubHits []string
			for canon, iface := range pub {
				if types.Implements(named, iface) || types.Implements(ptrNamed, iface) {
					pubHits = append(pubHits, strings.TrimSuffix(canon, ".Service"))
				}
			}
			var intlHits []string
			for canon, iface := range intl {
				if types.Implements(named, iface) || types.Implements(ptrNamed, iface) {
					intlHits = append(intlHits, strings.TrimSuffix(canon, ".Service"))
				}
			}

			if len(pubHits) > 0 && len(intlHits) > 0 {
				line := pass.Fset.Position(ts.Pos()).Line
				out = append(out, httpVisibilityViolation{
					Rel:               rel,
					Line:              line,
					TypeName:          pkgPath + "." + ts.Name.Name,
					PublicContracts:   pubHits,
					InternalContracts: intlHits,
				})
			}
		})
	}
	return out
}

// runHTTPContractVisibilityCheck loads the given patterns (which must include
// generated contract packages and production code in a single SharedResolver
// call), collects contract Service interfaces, and returns violations.
//
// patterns must be specific package patterns (not the literal "./...") so that
// PRODUCTION-LOADER-FUNNEL-01 does not apply. The caller is responsible for
// passing the right set of patterns.
func runHTTPContractVisibilityCheck(
	t *testing.T,
	tags []string,
	patterns []string,
) []httpVisibilityViolation {
	t.Helper()
	// Phase 1: collect contract Service interfaces from generated packages.
	// Phase 2: check production types against collected interfaces.
	// Both phases share the SAME SharedResolver call so types.Implements works
	// correctly across the two phases (same type-check universe).
	ifaceSet := newContractServiceIfaceSet()

	var violations []httpVisibilityViolation

	// Single RunTyped call covers all patterns. The SharedResolver caches the
	// packages.Load result, so two RunTyped calls with the same key share the
	// same type universe — types.Implements works across passes.
	//
	// Phase 1 pass: collect contract interfaces.
	phase1Diags := RunTyped(t, TypedOpts{Tests: false, Tags: tags}, patterns,
		func(pass *Pass) []Diagnostic {
			collectIfacesFromPass(pass, ifaceSet)
			return nil
		})
	_ = phase1Diags // Phase 1 never returns diagnostics; only side-effects on ifaceSet.

	// Phase 2 pass: check production struct types (SharedResolver returns cached result).
	RunTyped(t, TypedOpts{Tests: false, Tags: tags}, patterns,
		func(pass *Pass) []Diagnostic {
			violations = append(violations, checkPassForVisibilityViolations(pass, ifaceSet)...)
			return nil
		})

	return violations
}

// productionPatterns enumerates the package patterns that cover all production
// code (platform cells + example cells + runtime + adapters) together with the
// generated HTTP contract packages. This deliberately avoids the "./..." literal
// so that PRODUCTION-LOADER-FUNNEL-01 does not flag this call.
//
// The generated/ contracts are included because types.Implements requires the
// Service interface and the implementing type to come from the SAME go/types
// universe (single packages.Load call through SharedResolver).
var productionPatterns = []string{
	"./generated/contracts/http/...",
	"./cells/...",
	"./examples/...",
	"./runtime/...",
	"./adapters/...",
	"./kernel/...",
	"./pkg/...",
}

// INVARIANT: HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01
//
// TestHTTPContractVisibilityTypeSegregation01_RealRepoClean verifies that no
// production struct type simultaneously implements a generated public-path HTTP
// contract Service interface AND a generated internal-path HTTP contract Service
// interface. Detection capability is verified by the sibling
// TestHTTPContractVisibilityTypeSegregation01_ScannerCatchesViolation test.
func TestHTTPContractVisibilityTypeSegregation01_RealRepoClean(t *testing.T) {
	t.Parallel()

	violations := runHTTPContractVisibilityCheck(t, nil, productionPatterns)
	for _, v := range violations {
		t.Errorf(
			"HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01: %s:%d type %s "+
				"spans public/internal trust boundary at Go type level "+
				"(public: %v; internal: %v). "+
				"Extract per-slice Adapter types: public Adapters in "+
				"slices/<slice>/handler.go, internal Adapters in "+
				"slices/<internalslice>/handler.go. "+
				"ref: configread pattern "+
				"(cells/configcore/slices/configread+configreadinternal)",
			v.Rel, v.Line, v.TypeName, v.PublicContracts, v.InternalContracts,
		)
	}
}

// INVARIANT: HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01
//
// TestHTTPContractVisibilityTypeSegregation01_ScannerCatchesViolation verifies
// that the scanner correctly detects the pre-F1' monolithic Service shape: a
// single struct that simultaneously implements multiple public-path contract
// Service interfaces AND the internal-path contract Service interface.
//
// The fixture (tools/archtest/testdata/http_contract_visibility_type_segregation/)
// declares a MonolithicService struct with method implementations for all 6
// contract Service interfaces. It is gated by //go:build archtest_fixture so it
// does not compile into the main module build.
//
// Both the fixture package AND the generated contract packages are loaded in a
// single SharedResolver call (via the same combined patterns) so that
// types.Implements works correctly across the two groups — cross-graph
// interface resolution would silently return false.
func TestHTTPContractVisibilityTypeSegregation01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()

	fixturePatterns := []string{
		"./generated/contracts/http/...",
		"./tools/archtest/testdata/http_contract_visibility_type_segregation/...",
	}

	violations := runHTTPContractVisibilityCheck(
		t,
		[]string{"archtest_fixture"},
		fixturePatterns,
	)
	if len(violations) == 0 {
		t.Errorf(
			"HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01: fixture scan returned 0 violations — " +
				"scanner is broken or fixture does not implement the expected Service interfaces",
		)
		return
	}
	for _, v := range violations {
		t.Logf(
			"HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01: fixture correctly detected: "+
				"%s:%d type %s spans public=%v internal=%v",
			v.Rel, v.Line, v.TypeName, v.PublicContracts, v.InternalContracts,
		)
	}
}
