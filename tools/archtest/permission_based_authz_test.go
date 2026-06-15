//go:build archtest

// INVARIANT: PERMISSION-BASED-AUTHZ-01
//
// PERMISSION-BASED-AUTHZ-01 forbids hand-rolled role authorization in business
// handlers that have NOT been migrated to ABAC permission checks. Scope: corecells/
// AND examples/ (the latter added by PR-10d #1894 once iotdevice/todoorder migrated;
// it locks the migration so neither tree can regress). The rule has two arms:
//
//   - Helper-gate ban: a call whose receiver resolves (via go/types) to a local
//     alias of github.com/ghbvf/gocell/framework/runtime/auth and whose selector is
//     AnyRole, SelfOr, or RequireAnyRole. These gates hard-code a role identity at
//     the call site, bypassing the ABAC PDP (auth.RequirePermission + pkg/authz.Permission).
//   - HasRole semantic arm (PR-10d review F3): a call to (*auth.Principal).HasRole
//     in a non-allowlisted file. The helper ban only catches the helper SHAPE; a
//     migration could regress by hand-rolling the same role decision via p.HasRole
//     without ever touching AnyRole/SelfOr. This arm is type-aware on the receiver
//     (isPrincipalHasRoleCall), so it flags a caller role-authorization branch while
//     sparing same-named HasRole methods on other receivers (e.g. the rbaccheck
//     domain service.HasRole, whose purpose is to read a user's stored roles).
//     Sanctioned uses (PDP baselines + data-layer RowScope derivation) live in the
//     separate permissionBasedAuthzHasRoleAllowlist, each with a documented reason.
//
// # AI-robust grade: Medium
//
// Downstream Medium: type-aware CallExpr scan via ResolvePackageRef detects
// every call regardless of import alias; CI fails loud on any role-literal gate
// in any corecells OR examples business handler. The rule is NOT Hard because
// auth.AnyRole / auth.SelfOr remain callable (PR-10d removed their last callers
// but does NOT delete the helpers — that + codegen Hard-ification is PR-13) so
// the constraint cannot be expressed as a compile error — a handler can still
// type-check while calling them; only the typed scan rejects it. Extending the
// scan to examples/ (PR-10d) is a complementary Medium lock, NOT the PR-13
// Hard-ification (codegen funnel + golden), which remains a separate mechanism.
//
// Upstream funnel: the allowlist is a migration ledger, DRAINED to empty by
// PR-10c — the rule is a zero-exception scan over all corecells + examples
// handlers. Hard-ification end state: permission declared in contract.yaml →
// cellgen-derived gate + golden (Hard 范本 "codegen funnel + golden"), for PR-13.
//
// Funnel 双向锁评级 (ai-robust.md §"Funnel 双向锁评级"):
//
//	下游 Medium — archtest typed callsite scan, fail-loud in CI;
//	  alias-safe (ResolvePackageRef resolves the import path, not the local name).
//	上游 — allowlist convergence COMPLETE: PR-10a auditquery, PR-10b configcore,
//	  PR-10c accesscore all migrated to auth.RequirePermission /
//	  auth.RequirePermissionForResource (#1977 replaced RequirePermissionOrSelf
//	  when self/ownership moved into the PDP); the allowlist is empty.
//	Hard-ification tracker: permission-gate codegen funnel → gh #PR-13.
//
// # Allowlist (empty)
//
// The migration ledger is EMPTY as of PR-10c (#1348) and stays empty through
// PR-10d (#1894): every corecells business handler (auditquery, the 5 configcore
// slices, the 3 accesscore slices) AND every examples handler (iotdevice
// devicecommand/devicestatus/devicelist, todoorder order slices) uses a permission
// gate and must not regress. Any auth.AnyRole/SelfOr/RequireAnyRole call in a
// corecells or examples handler is now an unconditional violation.
//
// # Blind spots
//
//   - Method value capture (`p := auth.AnyRole; _ = p`): not flagged because
//     the assigned local is not itself a call. Production code does not use this
//     form; a future PR can extend the scan to SelectorExpr if needed.
//   - Dot-import (`import . ".../runtime/auth"`): ResolvePackageRef resolves
//     dot-imported bare identifiers via go/types info — this IS covered.
//   - Import alias (`import ra ".../runtime/auth"; ra.AnyRole(...)`):
//     ResolvePackageRef normalises the import path — this IS covered.
//   - Manual `p.Roles` iteration (`for _, r := range p.Roles { if r == role ... }`):
//     the HasRole arm flags the (*auth.Principal).HasRole METHOD, not an inlined
//     range over the exported Roles slice. corecells identitymanage's callerHasRole
//     uses the range form for a sanctioned field-level admin guard
//     (callerHasAdminAuthority, mirroring the PDP baseline per PR #1974 F1); it is NOT
//     caught here. Flagging arbitrary range-over-p.Roles is a much harder analysis
//     left out of scope — this arm targets the HasRole method specifically (review F3).
//
// Reverse self-check: TestPermissionBasedAuthz_ReverseFixture asserts the scan
// fires on testdata/permission_based_authz_red, proving the rule is not vacuous —
// the only anti-vacuity needed now that the allowlist is empty (with no
// allowlisted files, the production scan over ./corecells/... + ./examples/... is
// itself non-vacuous only if it can detect a violation, which the reverse fixture
// confirms).
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const rulePermissionBasedAuthz01 = "PERMISSION-BASED-AUTHZ-01"

// permissionBasedAuthzRoleGateSelectors is the set of auth.* selector names
// that constitute a role-literal authorization gate. All three wrap role-string
// arguments rather than ABAC permission identifiers.
var permissionBasedAuthzRoleGateSelectors = map[string]struct{}{
	"AnyRole":        {},
	"SelfOr":         {},
	"RequireAnyRole": {},
}

// permissionBasedAuthzAllowlist is the migration ledger: handler files that still
// legitimately call role-literal gates pending their PR-10x migration. Paths are
// module-relative slash paths (p.Rel values from the corecells module).
//
// EMPTY as of PR-10c (#1348): auditquery (PR-10a), the 5 configcore slices
// (PR-10b) and now the 3 accesscore slices (PR-10c) are all migrated to
// auth.RequirePermission / auth.RequirePermissionForResource (#1977 replaced
// RequirePermissionOrSelf with the PDP ownership gate). With an empty allowlist
// the rule is a ZERO-EXCEPTION scan: ANY auth.AnyRole/SelfOr/RequireAnyRole gate
// in a corecells business handler is a violation. The exact-set freeze survives the
// drain as TestPermissionBasedAuthzAllowlist_FrozenEmpty — now frozen at ∅ and run at
// PR-time (in-memory, NO packages.Load) — so a re-grow is rejected at PR-merge rather
// than left to reviewer vigilance (PR #1974 review F3). Hard-ification
// (contract.yaml → cellgen gate + golden) is tracked for PR-13.
var permissionBasedAuthzAllowlist = map[string]struct{}{}

// permissionBasedAuthzHasRoleAllowlist names the production files that legitimately
// branch on (*auth.Principal).HasRole. This is a SEPARATE ledger from
// permissionBasedAuthzAllowlist (which is frozen empty): HasRole is a method, not a
// helper gate, and the sanctioned uses below are NOT route-gate authorization — they
// are the PDP itself plus data-layer RowScope derivation. Keys are module-relative
// path SUFFIXES (the scan spans two modules — corecells + examples — so a suffix match
// is prefix-agnostic). Every entry must carry a live HasRole call (no-stale check) and
// a documented reason for why its role check is NOT a route-gate that belongs in the PDP.
var permissionBasedAuthzHasRoleAllowlist = map[string]string{
	// Example-owned PDP baselines: the authorizer IS the role→decision mapping (the
	// sanctioned home of role checks). The HTTP route gates and the gRPC handler call
	// INTO these via auth.RequirePermission / Authorize — they do not hand-roll the gate.
	"cells/devicecell/authorizer.go": "iotdevice example PDP baseline (deviceAuthorizer) — the role→decision mapping itself",
	"cells/ordercell/authorizer.go":  "todoorder example PDP baseline (orderAuthorizer) — the role→decision mapping itself",
	// Audit RowScope / FR-007 derivation: HasRole here selects the row-visibility
	// obligation (super-admin → CrossTenantVisibility vs RowVisibility) and the admin
	// audit breadcrumb. This is data-layer PEP / identity→RowScope derivation, which
	// tenancy.md sanctions as a framework-level concern distinct from the route gate.
	"slices/auditquery/handler.go": "audit RowScope/FR-007 + breadcrumb derivation (data-layer PEP, not a route gate)",
}

// scanPermissionBasedAuthzViolations scans a single file for role-literal
// authorization gate calls. A non-allowlisted file with a banned gate yields a
// violation. An allowlisted file with a banned gate yields NO violation but is
// recorded in observed (when non-nil) so the caller can prove the allowlist entry
// is still live (no-stale anti-vacuity) — an entry whose gate was already migrated
// away is then flagged after the full scan.
// It uses ResolvePackageRef to resolve the callee import path, so import aliases
// and dot-imports are handled uniformly.
func scanPermissionBasedAuthzViolations(p *Pass, f *ast.File, rel string, allowlist, observed map[string]struct{}) []Diagnostic {
	_, allowed := allowlist[rel]
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != authRuntimeImportPath {
			return
		}
		if _, banned := permissionBasedAuthzRoleGateSelectors[name]; !banned {
			return
		}
		if allowed {
			// Allowlisted: this entry is still live (carries a real role-literal
			// gate). Record it for the post-scan no-stale check; emit no violation.
			if observed != nil {
				observed[rel] = struct{}{}
			}
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(call.Pos()).Line,
			Message: "auth." + name + " role-literal gate in business handler violates " +
				rulePermissionBasedAuthz01 +
				"; migrate to auth.RequirePermission(authz.Perm*) and remove from allowlist",
		})
	})
	return out
}

// hasRoleAllowlistMatch reports whether rel ends with an allowlisted suffix and
// returns the matched suffix (used as the no-stale observation key).
func hasRoleAllowlistMatch(rel string) (suffix string, ok bool) {
	for s := range permissionBasedAuthzHasRoleAllowlist {
		if strings.HasSuffix(rel, s) {
			return s, true
		}
	}
	return "", false
}

// isPrincipalHasRoleCall reports whether call is a call to (*auth.Principal).HasRole
// — i.e. call.Fun is a selector `<recv>.HasRole` whose receiver type resolves (via
// go/types) to framework/runtime/auth.Principal. This receiver-type check is what
// makes the scan type-aware: it flags a hand-rolled principal role-authorization
// branch while sparing a same-named HasRole method on any other receiver (e.g. the
// rbaccheck domain service's own HasRole, which checks a user's stored roles and is
// the endpoint's whole business purpose, not an authorization branch on the caller).
func isPrincipalHasRoleCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "HasRole" {
		return false
	}
	recvType := info.TypeOf(sel.X)
	if recvType == nil {
		return false
	}
	// Production receiver is *auth.Principal (auth.FromContext returns it); strip the
	// pointer so the named-type check matches whether the method is called on T or *T.
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == authRuntimeImportPath && obj.Name() == "Principal"
}

// scanPermissionBasedAuthzHasRole scans one production file for hand-rolled
// (*auth.Principal).HasRole authorization branches. A non-allowlisted file with such
// a call yields a violation; an allowlisted file records the matched suffix in observed
// (for the no-stale check) and emits nothing. This is the semantic-equivalence arm of
// PERMISSION-BASED-AUTHZ-01: PERMISSION-BASED-AUTHZ's selector ban (AnyRole/SelfOr/...)
// only catches the helper SHAPE; a migration could regress by hand-rolling the same
// role decision via p.HasRole. The receiver-type check (isPrincipalHasRoleCall) keeps
// the scan from over-matching the rbaccheck service.HasRole domain method.
func scanPermissionBasedAuthzHasRole(p *Pass, f *ast.File, rel string, observed map[string]struct{}) []Diagnostic {
	suffix, allowed := hasRoleAllowlistMatch(rel)
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !isPrincipalHasRoleCall(p.TypesInfo, call) {
			return
		}
		if allowed {
			if observed != nil {
				observed[suffix] = struct{}{}
			}
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(call.Pos()).Line,
			Message: "(*auth.Principal).HasRole role-authorization branch in a business handler violates " +
				rulePermissionBasedAuthz01 +
				"; move the role decision into the ABAC PDP (auth.RequirePermission(authz.Perm*)). If this is a " +
				"sanctioned PDP baseline or data-layer/RowScope derivation, add the file to " +
				"permissionBasedAuthzHasRoleAllowlist with a documented reason",
		})
	})
	return out
}

// TestPermissionBasedAuthz_01 enforces PERMISSION-BASED-AUTHZ-01 over
// corecells business handlers. Any auth.AnyRole / auth.SelfOr /
// auth.RequireAnyRole call outside the migration allowlist is a violation.
//
// The scan is import-aware: ResolvePackageRef resolves the callee to its
// canonical go/types import path (github.com/ghbvf/gocell/framework/runtime/auth),
// so import aliases and dot-imports are all caught.
func TestPermissionBasedAuthz_01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// observed records every allowlisted file in which the scan saw a live
	// role-literal gate; the post-scan loop flags any allowlist entry NOT observed
	// as a STALE bypass slot (anti-vacuity — a migrated-away gate left in the
	// allowlist must not silently keep its exemption). observedHasRole is the same
	// no-stale ledger for the separate HasRole allowlist (keyed by matched suffix).
	observed := map[string]struct{}{}
	observedHasRole := map[string]struct{}{}

	diags := Run(t, Typed(TypedOpts{}, []string{"./corecells/...", "./examples/..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			var out []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				out = append(out, scanPermissionBasedAuthzViolations(p, f, rel, permissionBasedAuthzAllowlist, observed)...)
				out = append(out, scanPermissionBasedAuthzHasRole(p, f, rel, observedHasRole)...)
			}
			return out
		})

	// No-stale reverse self-check: every allowlist entry must have a live gate.
	for path := range permissionBasedAuthzAllowlist {
		if _, seen := observed[path]; !seen {
			diags = append(diags, Diagnostic{
				Rel: path,
				Message: "permissionBasedAuthzAllowlist entry " + path + " is STALE — no live " +
					"auth.AnyRole/SelfOr/RequireAnyRole gate observed there; the gate was migrated away or the " +
					"scanner regressed. Drop the dead allowlist entry so it cannot become a silent bypass slot",
			})
		}
	}
	// No-stale reverse self-check for the HasRole allowlist: every entry must have a
	// live (*auth.Principal).HasRole call, else it is a dead exemption to drop.
	for suffix := range permissionBasedAuthzHasRoleAllowlist {
		if _, seen := observedHasRole[suffix]; !seen {
			diags = append(diags, Diagnostic{
				Rel: suffix,
				Message: "permissionBasedAuthzHasRoleAllowlist entry " + suffix + " is STALE — no live " +
					"(*auth.Principal).HasRole call observed there; the branch was migrated away or the scanner " +
					"regressed. Drop the dead allowlist entry so it cannot become a silent bypass slot",
			})
		}
	}

	Report(t, rulePermissionBasedAuthz01, diags)
}

// TestPermissionBasedAuthz_ReverseFixture loads the synthetic RED fixture and
// asserts the scan fires — guards against the rule logic silently regressing to
// a vacuous pass. The fixture (testdata/permission_based_authz_red) is a
// standalone module with a handler that calls auth.AnyRole; it is NOT in the
// allowlist, so the scan must emit ≥ 1 diagnostic.
//
// Anti-vacuity: the production scan (TestPermissionBasedAuthz_01) is
// vacuously green for allowlisted files — this test proves the scanner can
// actually detect a violation when pointed at an un-allowlisted handler.
func TestPermissionBasedAuthz_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "permission_based_authz_red")

	emptyAllowlist := map[string]struct{}{}

	var diags []Diagnostic
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanPermissionBasedAuthzViolations(p, f, rel, emptyAllowlist, nil)...)
			}
			return nil
		})

	assert.GreaterOrEqual(t, len(diags), 1,
		"reverse fixture: expected ≥1 diagnostic for the role-literal auth.AnyRole callsite "+
			"in testdata/permission_based_authz_red — "+
			rulePermissionBasedAuthz01+" must fire, else the rule is vacuous")
}

// TestPermissionBasedAuthz_HasRole_ReverseFixture loads the synthetic RED fixture
// and asserts the HasRole arm of the scan fires on the hand-rolled
// (*auth.Principal).HasRole authorization branch while SPARING the GREEN control —
// a same-named HasRole method on a non-Principal receiver. Proves both anti-vacuity
// (the scan can fire) and no-over-match (the receiver-type discriminator works), so
// a green production run is meaningful.
func TestPermissionBasedAuthz_HasRole_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "permission_based_authz_red")

	var diags []Diagnostic
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				diags = append(diags, scanPermissionBasedAuthzHasRole(p, f, rel, nil)...)
			}
			return nil
		})

	// Exactly one: the principal role branch fires; the GREEN domain-service HasRole
	// call (non-Principal receiver) and the auth.AnyRole gate are both spared.
	assert.Len(t, diags, 1,
		"reverse fixture: expected exactly 1 diagnostic — the (*auth.Principal).HasRole branch must fire and "+
			"the non-Principal domain HasRole control must be spared; "+rulePermissionBasedAuthz01+
			" HasRole arm is vacuous or over-matching otherwise")
}

// TestPermissionBasedAuthzAllowlist_FrozenEmpty is the PR-time exact-set freeze on
// the (drained) migration allowlist. It is a pure in-memory assertion (NO
// packages.Load), so it is cheap enough for hack/verify-archtest-invariants.sh, and
// it restores the PR-time protection the deleted TestPermissionBasedAuthzAllowlist_Ceiling
// gave: with the allowlist at ∅ the freeze is "must stay empty", so a re-grow — a
// handler re-added to silently exempt a re-introduced auth.AnyRole/SelfOr/RequireAnyRole
// gate — fails at PR-merge, not only in the nightly whole-corecells scan
// (TestPermissionBasedAuthz_01). Without this guard the rule's only PR-time presence
// would be the non-vacuity reverse fixture, leaving allowlist re-growth to reviewer
// vigilance alone (a Soft regression). PR #1974 review F3.
func TestPermissionBasedAuthzAllowlist_FrozenEmpty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, permissionBasedAuthzAllowlist,
		"permissionBasedAuthzAllowlist must stay EMPTY (PR-10c drained it). Re-adding an entry "+
			"silently re-opens a role-literal-gate exemption — migrate the handler to "+
			"auth.RequirePermission(authz.Perm*) instead. If a future migration genuinely needs a "+
			"temporary ledger, restore the exact-set freeze (frozen expected-set compare) alongside it "+
			"so the allowlist still cannot grow/swap silently at PR-time")
}
