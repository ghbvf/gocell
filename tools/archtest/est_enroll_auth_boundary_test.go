//go:build archtest

// est_enroll_auth_boundary_test.go — residual auth-boundary guard for the
// device-identity EST enroll front-end (#1904 batch H).
//
// INVARIANT: EST-ENROLL-AUTH-BOUNDARY-01
//
// # What this guards (residual only)
//
// The enroll endpoint is contract-public (no listener JWT/ABAC gate) and is
// instead authenticated APPLICATION-layer by a dedicated enrollment credential
// (auth.TokenIntentEnrollment, verified by EnrollmentCredentialVerifier.Verify).
// Two Hard guards already exist and are NOT re-litigated here:
//
//   - #2020 codegen completeness forces every active HTTP contract to declare
//     exactly one AuthZ mode (enroll declares public + reason). Hard.
//   - FMT-28 restricts auth.bootstrap:true to /setup/admin paths. Hard.
//
// This Medium archtest guards the RESIDUAL the type system + codegen cannot
// express — the runtime wiring of the enroll handler:
//
//  1. MANDATORY: the deviceidentity package MUST actually verify the enrollment
//     credential, i.e. call (*auth.EnrollmentCredentialVerifier).Verify. A
//     contract-public route with no in-handler credential check is an
//     unauthenticated issuance endpoint. The ctx-injection wiring is invisible
//     to the contract layer (a ctx key cannot be typed at the route), so a
//     live-call scan is the only machine guard. Anti-vacuity: removing the
//     Verify call makes this test fail (0 references).
//  2. FORBIDDEN: the deviceidentity package MUST NOT reuse the setup-bootstrap
//     credential path (auth.NewBootstrapMiddleware / auth.BootstrapCredentials /
//     auth.Route.BootstrapAuth). Setup-bootstrap is operator basic-auth scoped
//     to one-time admin creation; reusing it for device enrollment is the
//     token-confusion vector the dedicated enrollment intent defends against.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
// Medium. Both prongs are type-aware go/types scans (info.Uses resolution to the
// exact framework/runtime/auth symbols), so detection is invariant to import /
// call-vs-value form. Honest caveat: Go cannot make "a public route must verify
// in-handler" a compile error (ctx injection is untyped), so enforcement is
// archtest-bound — the residual ceiling for this property (#2037 framework
// serving harness mounts the route; this guards what it wires).
//
// Anti-vacuity + RED fixture: the MANDATORY prong asserts ≥1 live Verify
// reference in real deviceidentity code (a regressed resolver reports 0 → fail).
// The FORBIDDEN prong is proven live by internal/estenrollauthfixture, which
// reuses the bootstrap middleware and MUST be flagged.
//
// Blind spot (known, by design): the scan excludes _test.go files. Test doubles
// may construct verifiers / bootstrap creds freely — production-only enforcement
// is the intended boundary.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// estAuthPkgPath is the package declaring the enrollment-credential verifier
	// and the bootstrap-auth symbols.
	estAuthPkgPath = PlatformFrameworkModulePath + "/runtime/auth"

	// enrollVerifierTypeName is the dedicated enrollment-credential verifier type.
	enrollVerifierTypeName = "EnrollmentCredentialVerifier"
	enrollVerifyMethodName = "Verify"

	// estDeviceIdentityPattern scopes the scan to the EST front-end package.
	estDeviceIdentityPattern = "./cellmodules/deviceidentity/..."
)

// estBootstrapForbiddenSymbols is the set of framework/runtime/auth setup-bootstrap
// symbols the deviceidentity enroll front-end must NOT reference (reuse vector).
var estBootstrapForbiddenSymbols = map[string]struct{}{
	"NewBootstrapMiddleware": {},
	"BootstrapCredentials":   {},
	"BootstrapAuth":          {}, // auth.Route.BootstrapAuth field
}

// TestEstEnrollAuthBoundary01_EnrollVerifies (MANDATORY prong): the deviceidentity
// package must contain at least one live reference to
// (*auth.EnrollmentCredentialVerifier).Verify. Zero = the public enroll route is
// not actually authenticating the enrollment credential (anti-vacuity).
func TestEstEnrollAuthBoundary01_EnrollVerifies(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var verifyRefs int
	_ = Run(t, Typed(TypedOpts{}, []string{estDeviceIdentityPattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if isEnrollVerifyRef(p.TypesInfo, id) {
					verifyRefs++
				}
			})
		}
		return nil
	})
	assert.GreaterOrEqual(t, verifyRefs, 1,
		"EST-ENROLL-AUTH-BOUNDARY-01 (mandatory): deviceidentity production code has %d references to "+
			"(*auth.EnrollmentCredentialVerifier).Verify, want ≥ 1. A contract-public enroll route with no "+
			"in-handler enrollment-credential verification is an UNAUTHENTICATED certificate-issuance endpoint. "+
			"Restore the verifier call in the enroll auth middleware.", verifyRefs)
}

// TestEstEnrollAuthBoundary01_NoBootstrapReuse (FORBIDDEN prong): the
// deviceidentity package must not reference any setup-bootstrap auth symbol.
func TestEstEnrollAuthBoundary01_NoBootstrapReuse(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Typed(TypedOpts{}, []string{estDeviceIdentityPattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			d = append(d, scanEstBootstrapReuse(p, file, rel)...)
		}
		return d
	})
	Report(t, "EST-ENROLL-AUTH-BOUNDARY-01", diags)
}

// TestEstEnrollAuthBoundary01_RedFixture is the reverse self-check: the fixture
// reuses the setup-bootstrap middleware from a deviceidentity-like package; the
// forbidden-symbol detector must flag it. A 0 result means the detector regressed.
func TestEstEnrollAuthBoundary01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	fixturePkg := modPath + "/tools/archtest/internal/estenrollauthfixture"
	pattern := "./tools/archtest/internal/estenrollauthfixture/..."

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanEstBootstrapReuse(p, file, p.Rel(file)))
		}
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag a setup-bootstrap auth reference in the enroll front-end")
}

// scanEstBootstrapReuse flags every identifier USE that resolves to a
// framework/runtime/auth setup-bootstrap symbol (estBootstrapForbiddenSymbols).
func scanEstBootstrapReuse(p *Pass, file *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if !isEstBootstrapSymbolRef(p.TypesInfo, id) {
			return
		}
		pos := p.Fset.Position(id.Pos())
		d = append(d, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"EST-ENROLL-AUTH-BOUNDARY-01 (forbidden): deviceidentity references auth.%s — the device "+
					"enroll front-end must authenticate with the dedicated enrollment credential "+
					"(EnrollmentCredentialVerifier), NOT the setup-bootstrap operator path. Reusing "+
					"setup-bootstrap for enrollment is a token-confusion bypass (FMT-28 restricts bootstrap "+
					"to /setup/admin).", id.Name,
			),
		})
	})
	return d
}

// isEnrollVerifyRef resolves an identifier USE to
// (*auth.EnrollmentCredentialVerifier).Verify via go/types (info.Uses).
func isEnrollVerifyRef(info *types.Info, id *ast.Ident) bool {
	if id.Name != enrollVerifyMethodName {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != estAuthPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type()
	if ptr, isPtr := recv.(*types.Pointer); isPtr {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	return ok && named.Obj().Name() == enrollVerifierTypeName
}

// isEstBootstrapSymbolRef resolves an identifier USE to a framework/runtime/auth
// setup-bootstrap symbol in estBootstrapForbiddenSymbols (func, type, or field).
func isEstBootstrapSymbolRef(info *types.Info, id *ast.Ident) bool {
	if _, forbidden := estBootstrapForbiddenSymbols[id.Name]; !forbidden {
		return false
	}
	obj := info.Uses[id]
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != estAuthPkgPath {
		return false
	}
	switch obj.(type) {
	case *types.Func, *types.TypeName, *types.Var:
		return true
	default:
		return false
	}
}
