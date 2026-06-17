// Importable rule body for CELLTLS-MATERIAL-FUNNEL-01. Non-test .go file so the
// rule is module-path-agnostic — platform symbol paths are derived from
// [PlatformFrameworkModulePath] (external.go), NOT bare
// "github.com/ghbvf/gocell/framework…" literals
// (ARCHTEST-MODULE-PATH-FUNNEL-01). The dogfood + RED-fixture precision gate
// live in celltls_material_funnel_test.go.
//
// Not registered in StandardCellRules: the sanctioned provisioning sites
// (cellmodules/celltls, adapters/grpc) and the banned tlsutil mTLS-material
// constructors are GoCell-internal packages; an external Cell repo has no such
// files, so the allowlist never matches and the rule degrades to a pure ban
// that would false-red any external module that legitimately calls the
// tlsutil helpers from its own provisioning site. Kept importable +
// module-path-agnostic but OUT of StandardCellRules (same disposition as
// CAPABILITY-PROVIDER-FUNNEL-01 and the #1632 auth funnels).
//
// # CELLTLS-MATERIAL-FUNNEL-01
//
// The transport mTLS-material constructors
//
//	framework/runtime/http/tlsutil.NewClientIdentity
//	framework/runtime/http/tlsutil.NewServerMTLSConfig
//
// may only be called from the following sanctioned sites (verified by repo
// scan on 2026-06-17, issue #2263):
//
//   - cellmodules/celltls (celltls.Resolve) — cell-to-cell HTTP mTLS for the
//     internal listener in split topology (the primary funnel site; topology-
//     gated, fail-closed). Calls both NewClientIdentity and NewServerMTLSConfig.
//   - adapters/grpc (buildCredentials) — gRPC listener transport-layer mTLS:
//     a separate TLS channel provisioned directly from PEM bytes in the gRPC
//     server TLSConfig, independent of the HTTP cross-cell transport. Calls
//     NewServerMTLSConfig only; NewClientIdentity is NOT used here (the gRPC
//     transport-layer does not build the ClientIdentity sealed bundle).
//
// Every other production caller is a violation.
//
// # AI-robust: upstream Hard + downstream Medium (Go-ceiling transition form)
//
// Upstream is Hard: [tlsutil.ClientIdentity] has only unexported fields; the
// sole constructor is [tlsutil.NewClientIdentity]; outside package tlsutil a
// zero-value ClientIdentity.IsZero() call is the only shape available —
// forging a populated value (and therefore bypassing this funnel) is a compile
// error. Downstream is Medium: archtest resolves every CallExpr callee via
// [ResolvePackageRef] (owning package import path from go/types, not the
// source Ident), so import aliases don't defeat it. Per ai-robust.md §"Funnel
// 双向锁评级" a Hard-upstream + Medium-downstream funnel is the accepted
// transition form; the downstream→Hard upgrade path is the same as
// CAPABILITY-PROVIDER-FUNNEL-01 (gh #988).
//
// # _test.go scope
//
// Production(TypedOpts{Tests: false}) loads only production-variant packages,
// so _test.go files are not in pass.Files; the scanner additionally filters
// by rel suffix as defense-in-depth — so the rule stays correct if a future
// caller passes Tests: true or runs it over a fixture.
//
// # Blind spots (BS)
//
//   - BS-1 Name shadowing: a non-tlsutil package exporting a function literally
//     named NewClientIdentity or NewServerMTLSConfig does NOT match —
//     ResolvePackageRef compares the callee's owning package path via
//     go/types, not the import-site Ident.
//   - BS-2 Function-value indirection: `var f = tlsutil.NewClientIdentity;
//     f(...)` resolves to *types.Var (ok=false) and is not flagged. Accepted:
//     the sanctioned callers don't do this.
//   - BS-3 Reflection construction: out of scope per ai-robust.md §3.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	cellTLSFunnelRuleID = "CELLTLS-MATERIAL-FUNNEL-01"

	// cellTLSUtilImportPath is the import path of the tlsutil package, derived
	// from PlatformFrameworkModulePath so a module rename updates one place
	// (ARCHTEST-MODULE-PATH-FUNNEL-01).
	cellTLSUtilImportPath = PlatformFrameworkModulePath + "/runtime/http/tlsutil"

	// cellTLSSanctionedCellTLSPkg is the import path of the primary sanctioned
	// caller (cellmodules/celltls). Derived from PlatformModulePath (cellmodules
	// is a SIBLING module of framework, not under it).
	cellTLSSanctionedCellTLSPkg = PlatformModulePath + "/cellmodules/celltls"

	// cellTLSSanctionedGRPCPkg is the import path of the secondary sanctioned
	// caller (adapters/grpc). Calls NewServerMTLSConfig only (not
	// NewClientIdentity). Verified by repo scan on 2026-06-17 / #2263.
	cellTLSSanctionedGRPCPkg = PlatformModulePath + "/adapters/grpc"
)

// cellTLSBannedCtors is the closed set of tlsutil mTLS-material constructors
// that may only be called from the sanctioned packages.
var cellTLSBannedCtors = map[string]struct{}{
	"NewClientIdentity":   {},
	"NewServerMTLSConfig": {},
}

// CheckCellTLSMaterialFunnel enforces CELLTLS-MATERIAL-FUNNEL-01: the
// tlsutil mTLS-material constructors NewClientIdentity and NewServerMTLSConfig
// may only be called from the sanctioned sites (cellmodules/celltls and
// adapters/grpc). It scans the running module's full production tree (all
// packages, generated/ excluded) and returns the diagnostics it observes;
// GoCell's TestCellTLSMaterialFunnel calls it directly — single source, no
// parallel rule body.
func CheckCellTLSMaterialFunnel(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Production(TypedOpts{Tests: false}), scanCellTLSMaterialViolations)
}

// scanCellTLSMaterialViolations walks every CallExpr in pass.Files, resolves
// the callee to its (pkgPath, name) tuple via ResolvePackageRef, and flags
// hits whose owning package is cellTLSUtilImportPath and whose name is in
// cellTLSBannedCtors — unless the call is inside a sanctioned site or a
// _test.go file.
func scanCellTLSMaterialViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	pkgPath := ""
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if isCellTLSSanctionedSite(pkgPath) {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			calleePkg, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok {
				return
			}
			if calleePkg != cellTLSUtilImportPath {
				return
			}
			if _, banned := cellTLSBannedCtors[name]; !banned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"tlsutil.%s is a mTLS-material constructor; "+
						"production callers are restricted to cellmodules/celltls and adapters/grpc "+
						"(%s). "+
						"For cell-to-cell HTTP mTLS use celltls.Resolve; "+
						"for gRPC transport-layer mTLS use adapters/grpc.TLSConfig",
					name, cellTLSFunnelRuleID,
				),
			})
		})
	}
	return out
}

// isCellTLSSanctionedSite reports whether pkgPath is one of the two sanctioned
// callers of the tlsutil mTLS-material constructors. Binding the exemption to
// the exact package path (not a relative file path) means a consumer module
// that wires this importable rule via cfg.ExtraRules and forges the same
// relative package name is NOT exempt — its pkgPath is under the consumer's
// own module, not PlatformModulePath. Extracted as a pure function for unit
// testability (TestIsCellTLSSanctionedSite).
func isCellTLSSanctionedSite(pkgPath string) bool {
	return pkgPath == cellTLSSanctionedCellTLSPkg ||
		pkgPath == cellTLSSanctionedGRPCPkg
}
