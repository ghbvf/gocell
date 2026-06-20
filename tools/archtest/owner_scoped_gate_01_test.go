//go:build archtest

// INVARIANT: OWNER-SCOPED-GATE-EXACT-SET-01
//
// OWNER-SCOPED-GATE-EXACT-SET-01 freezes the set of owner-scoped route gates. It has
// TWO arms (split by enforcement mechanism, #2355):
//
//   - TestOwnerScopedGate_ContractDerived_01 (metadata) — accesscore's identitymanage/
//     rbaccheck/authorizationdecide gates migrated to CONTRACT-DERIVED authz: declared in
//     contract.yaml endpoints.http.{resource,selfScoped} and rendered into the generated
//     handler_gen.go contractSpec via the single RequirePermissionForContract funnel
//     (golden-locked Hard). Frozen against ownerScopedGateContractDerivedSet.
//   - TestOwnerScopedGate_ExactSet_01 (scan) — the STILL-hand-wired gates: the
//     iotdevice/todoorder examples (PR-10d #1894, auth.SelfOr → RequirePermissionForResource)
//     AND the composition-root cellmodules/deviceserving framework-owned devicestate gate
//     (#2351). Frozen against ownerScopedGateExpectedSet. As these migrate (#2355 续波 #2486) they
//     move to the contract-derived arm.
//
// An owner-scoped endpoint (one whose resource ownership the PDP
// decides via the baseline rule subject.sub == resource.id, #1977) MUST gate with
// one of the two sanctioned owner-scoped gate shapes (hand-wired, scan arm) or declare
// the equivalent endpoints.http.{resource,selfScoped} overlay (contract-derived arm):
//
//	auth.RequirePermissionForResource("<pathParam>", authz.Perm*())  // resource = path param
//	auth.RequirePermissionForSelf(authz.Perm*())                     // resource = caller's own subject (#1863)
//
// Both forward a canonical resource id to the PDP as the `resource` argument
// (RequirePermissionForResource canonicalizes the path-param id; RequirePermissionForSelf
// forwards the caller's own subject — used by POST /api/v1/access/decide, which has no
// path param). The frozen expected set (ownerScopedGateExpectedSet) is the exact list of
// (handler, param, permission-accessor) triples, where param is the path-param name or the
// literal "self".
//
// The threat this closes (PR #2025 review F1): tenancy.md mandates this gate shape,
// but PERMISSION-BASED-AUTHZ-01 only BANS role-literal gates — it cannot detect an
// owner endpoint silently regressing from RequirePermissionForResource to plain
// auth.RequirePermission(perm). Plain RequirePermission forwards r.URL.Path (not the
// canonical resource id), so the ownership rule never matches and self-access breaks
// (or, with a permissive policy, widens). A regression drops the endpoint's triple
// from the collected set → exact-set mismatch → CI fails.
//
// # AI-robust grade: per arm
//
// Contract-derived arm (accesscore, #2355): the gate behavior is 100% derived from the
// generated contractSpec.{Resource,SelfScoped} via the single RequirePermissionForContract
// funnel — codegen funnel + byte golden = HARD (the Hard 范本 "codegen funnel + golden",
// the end state long tracked for PERMISSION-BASED-AUTHZ-01 / PR-13, now realized for
// accesscore). TestOwnerScopedGate_ContractDerived_01 is the MEDIUM reverse-check that the
// metadata declarations (which contracts are owner/self-scoped) stay frozen.
//
// Scan arm (still-hand-wired examples + deviceserving): MEDIUM — type-aware CallExpr scan
// via ResolvePackageRef (alias/dot-import safe) collects every
// RequirePermissionForResource(strlit, authz.Perm*()) / RequirePermissionForSelf triple in
// the guarded handler files and asserts the collected set EQUALS the frozen set. NOT Hard:
// nothing in the type system forces an owner endpoint to choose RequirePermissionForResource
// over RequirePermission (both type-check); only this scan rejects the wrong choice. These
// migrate to the contract-derived (Hard) arm as #2355 续波 (#2486 examples, #2487 auditcore-list) lands.
//
// # Anti-vacuity
//
// This is a PRESENCE exact-set scan, not a ban scan: a green result PROVES the scanner
// works, because it can only be green when the scan collected exactly the frozen
// triples. Two built-in discriminators make it non-vacuous without a ban-style reverse
// fixture:
//
//   - The example handlers carry the discriminator (#2355: accesscore's identitymanage,
//     formerly the canonical example, migrated off the scan): ordercell/cell.go and
//     devicecell/cell.go each hold plain RequirePermission gates (create/list, device:list)
//     alongside the owner gates, so over-collection would surface as an UNEXPECTED triple.
//   - If the typed scan silently failed to resolve any callsite, the collected set would be
//     empty and every frozen triple would report MISSING → fail.
//
// TestOwnerScopedGate_ReverseFixture additionally scans a standalone RED module that
// (a) drifts a gate's path param and (b) regresses an owner gate to plain
// RequirePermission, proving param drift is observed as a changed triple and a plain-gate
// regression is observed as a MISSING triple — the two real-world drift modes.
//
// # Blind spots
//
//   - Guards gate CONSTRUCTION, not route→gate WIRING: a correctly-constructed gate that
//     is never mounted (or mounted on the wrong handler) is not caught here — the
//     contract serve tests + e2e cover wiring.
//   - Scan arm: only the named hand-wired handler files are scanned (examples
//     ordercell/cell.go, devicecell/cell.go, devicecommand/handler.go + the composition-root
//     cellmodules/deviceserving/service.go, #2351); a NEW hand-wired owner-scoped endpoint in
//     a new file must be added to ownerScopedGateHandlerKey + ownerScopedGateExpectedSet (the
//     UNEXPECTED-triple check forces this consciously for the already-guarded files). A
//     RequirePermissionForSelf callsite in an unlisted file is likewise not frozen. The
//     contract-derived arm has no such file-list blind spot — it scans ALL project metadata.
//   - Contract-derived arm: guards that a contract KEEPS its resource/selfScoped overlay
//     (and which param/permission), but — like the FMT-42 DELIBERATE non-port of gRPC FMT-41
//     — it CANNOT tell whether a brand-NEW route SHOULD be owner-scoped: the same action
//     (e.g. user:write) gates both owner routes (with resource) and admin routes (without),
//     so owner-vs-admin intent on a new route is a per-route authoring choice with no machine
//     enforcement (irreducible, see ADR 202606201500-2355). This frozen set catches
//     regressions on KNOWN routes; new-route intent relies on the contract 403 description +
//     ownership serve test + review.
//   - Scan SCOPE vs handler-key ASYMMETRY (#2351): the scan now loads all of
//     ./cellmodules/... but ownerScopedGateHandlerKey only recognizes deviceserving/service.go;
//     an owner gate added in ANY OTHER cellmodules file resolves handler="" and is SILENTLY
//     SKIPPED (not flagged UNEXPECTED — unscanned ≠ unexpected). The widened scope does NOT
//     mean "all of cellmodules is guarded"; only the explicitly keyed files are. A new
//     cellmodules owner gate MUST add its handler key here. (Hard end state: contract-derived
//     gate + codegen golden, PR-13, removes the manual file list entirely.)
package archtest

import (
	"go/ast"
	"go/constant"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleOwnerScopedGateExactSet01 = "OWNER-SCOPED-GATE-EXACT-SET-01"

// authzImportPath is the canonical import path of the sealed permission registry
// (pkg/authz). The second argument of every owner-scoped gate must be a call to one
// of its Perm*() accessor functions.
const authzImportPath = PlatformFrameworkModulePath + "/pkg/authz"

// ownerScopedGateExpectedSet is the FROZEN set of owner-scoped route gates, keyed
// "<handler>|<pathParam>|<permAccessor>". Adding/removing an owner endpoint, or
// changing its path param or permission, must update this set in the same change —
// otherwise the exact-set compare fails. See the file godoc for the invariant.
var ownerScopedGateExpectedSet = map[string]struct{}{
	// NOTE (#2355 / #2486): accesscore (identitymanage/rbaccheck/authorizationdecide)
	// AND the examples (iotdevice devicecell + todoorder ordercell) MIGRATED off
	// hand-wired gates to contract-derived authz — their owner/self-scoped shape is now
	// declared in contract.yaml endpoints.http.{resource,selfScoped} and rendered into
	// the generated handler_gen.go contractSpec via the single RequirePermissionForContract
	// funnel (golden-locked Hard). They are guarded by the metadata-derived
	// TestOwnerScopedGate_ContractDerived_01 below, NOT by this scan. This scan now covers
	// only the LAST still-hand-wired owner gate: deviceserving (#2351). When that one
	// migrates too, this set empties and the scan arm can retire.
	//
	// #2351: the framework-owned http.devicestate.v1 serving gate lives in the
	// composition-root layer (cellmodules/deviceserving/service.go), not a cell handler —
	// the first owner-scoped gate frozen outside corecells/examples (scan scope widened to
	// ./cellmodules/... below). Device-SELF ownership (subject.sub == resource.id), backed
	// by the accesscore baseline-device-read-self rule. A regression to plain
	// auth.RequirePermission would forward r.URL.Path, breaking per-device ownership and
	// re-opening the cross-device enumeration vector → this triple goes MISSING → CI red.
	"deviceserving|id|PermDeviceRead": {},
}

// ownerScopedGateContractDerivedSet is the FROZEN set of owner-scoped / self-scoped
// HTTP gates that migrated to contract-derived authz (accesscore #2355, examples #2486),
// keyed "<contractID>|<param-or-self>|<action>" where param is the endpoints.http.resource
// path-param name (owner-scoped) or the literal "self" (endpoints.http.selfScoped), and
// action is the endpoints.http.permission string. The live set derived from project
// metadata MUST equal this exactly (TestOwnerScopedGate_ContractDerived_01): dropping a
// contract's resource/selfScoped overlay (re-coarsening its gate) shrinks the live set →
// MISSING → CI red; adding/changing one without updating this set → UNEXPECTED → CI red.
// The gate behavior itself is golden-locked at codegen (the contractSpec literal); this is
// the Medium reverse-check that the metadata declarations stay frozen.
var ownerScopedGateContractDerivedSet = map[string]struct{}{
	// accesscore (#2355)
	"http.auth.user.get.v1|id|user:read":              {},
	"http.auth.user.update.v1|id|user:write":          {},
	"http.auth.user.patch.v1|id|user:write":           {},
	"http.auth.user.change-password.v1|id|user:write": {},
	"http.auth.role.list.v1|userID|role:read":         {},
	"http.auth.role.check.v1|userID|role:read":        {},
	"http.auth.decide.v1|self|access:decide":          {},
	// examples: todoorder ordercell (#2486)
	"http.order.get.v1|id|order:read":       {},
	"http.order.confirm.v1|id|order:update": {},
	// examples: iotdevice devicecell (#2486)
	"http.device.status.v1|id|device:read":                  {},
	"http.device.command.dequeue.v1|id|device:consume":      {},
	"http.device.command.report.v1|id|device:consume":       {},
	"http.device.command.ack.v1|id|device:consume":          {},
	"http.device.command.extend-lease.v1|id|device:consume": {},
}

// ownerScopedGateHandlerKey maps a module-relative handler path to its short key,
// or "" if the file is not one of the owner-scoped handlers under guard.
func ownerScopedGateHandlerKey(rel string) string {
	switch {
	// NOTE (#2355): accesscore identitymanage/rbaccheck/authorizationdecide migrated to
	// contract-derived authz — no longer hand-wired, so no longer scanned here (guarded by
	// TestOwnerScopedGate_ContractDerived_01 via project metadata instead).
	//
	// examples (PR-10d #1894). todoorder get+confirm owner gates both live in
	// ordercell/cell.go — one handler key, the two triples differ by permission
	// (PermOrderRead/PermOrderUpdate). iotdevice's status owner gate lives in
	// devicecell/cell.go; the devicecommand consume gate lives in its slice handler.
	case strings.HasSuffix(rel, "cells/ordercell/cell.go"):
		return "todoorder-order"
	case strings.HasSuffix(rel, "cells/devicecell/cell.go"):
		return "iotdevice-device"
	case strings.HasSuffix(rel, "slices/devicecommand/handler.go"):
		return "devicecommand"
	// #2351: the framework-owned devicestate serving gate in the composition-root layer
	// (cellmodules/deviceserving/service.go Route()), gated by
	// RequirePermissionForResource("id", PermDeviceRead()). Scanned because the scan scope
	// includes ./cellmodules/... (see TestOwnerScopedGate_ExactSet_01).
	case strings.HasSuffix(rel, "deviceserving/service.go"):
		return "deviceserving"
	default:
		return ""
	}
}

// ownerScopedGateConstString returns the compile-time string value of expr (handles
// string literals and const-bound identifiers via go/types constant folding), or
// ("", false) if expr is not a constant string.
func ownerScopedGateConstString(p *Pass, expr ast.Expr) (string, bool) {
	tv, ok := p.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// ownerScopedGateAccessor returns the pkg/authz Perm*() accessor name of arg, or
// the "<non-authz-accessor>" sentinel when arg is not a call to a pkg/authz
// accessor (so a wrong permission argument surfaces as a changed triple rather
// than being silently skipped).
func ownerScopedGateAccessor(p *Pass, arg ast.Expr) string {
	if argCall, ok := arg.(*ast.CallExpr); ok {
		if apath, aname, ok := ResolvePackageRef(p.TypesInfo, argCall.Fun); ok && apath == authzImportPath {
			return aname
		}
	}
	return "<non-authz-accessor>"
}

// collectOwnerScopedGates scans one file for the two owner-scoped gate shapes and
// returns a "<handler>|<param>|<permAccessor>" key for each:
//
//   - auth.RequirePermissionForResource(pathParam, authz.Perm*()) → param is the
//     canonicalized path-param name (resource id forwarded to the PDP);
//   - auth.RequirePermissionForSelf(authz.Perm*()) → param is the literal "self"
//     (#1863: the gate forwards the caller's OWN subject as resource, no path
//     param; the canonical use is POST /api/v1/access/decide gated on access:decide).
//
// Both forward a canonical resource id to the PDP so the baseline
// subject.sub == resource.id rule decides ownership; a silent regression of either
// to plain auth.RequirePermission (which forwards r.URL.Path) drops the triple →
// MISSING → CI red. Only calls whose receiver resolves to runtime/auth are
// recognized; a non-const param, wrong arity, or non-authz accessor is recorded
// with a sentinel so it surfaces as an UNEXPECTED triple rather than being silently
// skipped.
func collectOwnerScopedGates(p *Pass, f *ast.File, handler string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok || pkgPath != authRuntimeImportPath {
			return
		}
		switch name {
		case "RequirePermissionForResource":
			if len(call.Args) != 2 {
				out = append(out, handler+"|<bad-arity>|<bad-arity>")
				return
			}
			param, ok := ownerScopedGateConstString(p, call.Args[0])
			if !ok {
				param = "<non-const-param>"
			}
			out = append(out, handler+"|"+param+"|"+ownerScopedGateAccessor(p, call.Args[1]))
		case "RequirePermissionForSelf":
			if len(call.Args) != 1 {
				out = append(out, handler+"|<bad-arity>|<bad-arity>")
				return
			}
			out = append(out, handler+"|self|"+ownerScopedGateAccessor(p, call.Args[0]))
		}
	})
	return out
}

// TestOwnerScopedGate_ExactSet_01 enforces OWNER-SCOPED-GATE-EXACT-SET-01: the
// collected set of owner-scoped RequirePermissionForResource triples across the
// guarded accesscore handler files must EQUAL ownerScopedGateExpectedSet.
func TestOwnerScopedGate_ExactSet_01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	collected := map[string]struct{}{}
	_ = Run(t, Typed(TypedOpts{}, []string{"./corecells/...", "./examples/...", "./cellmodules/..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				handler := ownerScopedGateHandlerKey(rel)
				if handler == "" {
					continue
				}
				for _, k := range collectOwnerScopedGates(p, f, handler) {
					collected[k] = struct{}{}
				}
			}
			return nil
		})

	var diags []Diagnostic
	for k := range ownerScopedGateExpectedSet {
		if _, ok := collected[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "MISSING owner-scoped gate " + k + " — an owner endpoint regressed off " +
					"auth.RequirePermissionForResource(pathParam, authz.Perm*()) or changed its param/permission; " +
					"ownership would no longer be PDP-decided (" + ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	for k := range collected {
		if _, ok := ownerScopedGateExpectedSet[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "UNEXPECTED owner-scoped gate " + k + " — a new/changed owner endpoint must be added " +
					"to ownerScopedGateExpectedSet (the frozen set) in the same change (" + ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	Report(t, ruleOwnerScopedGateExactSet01, diags)
}

// TestOwnerScopedGate_ContractDerived_01 is the #2355 contract-derived arm of
// OWNER-SCOPED-GATE-EXACT-SET-01: accesscore's owner/self-scoped gates are no longer
// hand-wired but declared in contract.yaml endpoints.http.{resource,selfScoped} and
// rendered into the generated handler_gen.go contractSpec via the single
// RequirePermissionForContract funnel (golden-locked Hard). This test freezes the live
// set of metadata-declared owner/self-scoped gates against
// ownerScopedGateContractDerivedSet — so dropping a contract's resource/selfScoped overlay
// (re-coarsening the gate) shrinks the live set → MISSING, and adding/changing one →
// UNEXPECTED. The gate behavior is golden-locked at codegen (Hard); this is the Medium
// reverse-check that the metadata declarations stay frozen.
func TestOwnerScopedGate_ContractDerived_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	httpCount := 0
	collected := map[string]struct{}{}
	for _, c := range project.Contracts {
		if c.Kind != "http" {
			continue
		}
		httpCount++
		if c.Lifecycle != "active" || !c.Codegen {
			continue
		}
		h := c.Endpoints.HTTP
		if h == nil {
			continue
		}
		switch {
		case h.Resource != "":
			collected[c.ID+"|"+h.Resource+"|"+h.Permission] = struct{}{}
		case h.SelfScoped:
			collected[c.ID+"|self|"+h.Permission] = struct{}{}
		}
	}
	// Anti-vacuity: the loader must see the project's HTTP contracts AND at least the
	// frozen owner/self gates, else the exact-set compare would pass against an empty set.
	// The frozen set spans BOTH resource (owner) and selfScoped (self) shapes, so a loader
	// that parsed only one would report the other MISSING — a built-in discriminator.
	if httpCount == 0 {
		t.Fatal("anti-vacuity: parsed zero http contracts — project loader misconfigured")
	}
	if len(collected) == 0 {
		t.Fatal("anti-vacuity: parsed zero contract-derived owner/self-scoped gates — overlay loader misconfigured")
	}

	var diags []Diagnostic
	for k := range ownerScopedGateContractDerivedSet {
		if _, ok := collected[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "MISSING contract-derived owner/self-scoped gate " + k + " — a contract dropped its " +
					"endpoints.http.{resource,selfScoped} overlay (re-coarsening the gate) or changed its param/permission; " +
					"ownership would no longer be PDP-decided (" + ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	for k := range collected {
		if _, ok := ownerScopedGateContractDerivedSet[k]; !ok {
			diags = append(diags, Diagnostic{
				Message: "UNEXPECTED contract-derived owner/self-scoped gate " + k + " — a new/changed owner contract " +
					"must be added to ownerScopedGateContractDerivedSet (the frozen set) in the same change (" +
					ruleOwnerScopedGateExactSet01 + ")",
			})
		}
	}
	Report(t, ruleOwnerScopedGateExactSet01, diags)
}

// TestOwnerScopedGate_ReverseFixture scans the standalone RED module and asserts the
// two real drift modes are observable: a path-param drift surfaces as a changed
// triple, and an owner gate regressed to plain auth.RequirePermission surfaces as a
// MISSING RequirePermissionForResource triple (it is not collected at all). Proves
// the production exact-set compare is not vacuous.
func TestOwnerScopedGate_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "owner_scoped_gate_red")

	collected := map[string]struct{}{}
	_ = Run(t, StandaloneModule(fixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				for _, k := range collectOwnerScopedGates(p, f, "redfixture") {
					collected[k] = struct{}{}
				}
			}
			return nil
		})

	// Param drift is observed as a changed triple.
	assert.Contains(t, collected, "redfixture|wrongParam|PermUserWrite",
		"reverse fixture: a path-param drift must surface as a changed triple — "+
			ruleOwnerScopedGateExactSet01+" extraction is vacuous otherwise")
	// The correctly-shaped gate is collected.
	assert.Contains(t, collected, "redfixture|id|PermUserRead",
		"reverse fixture: a well-formed owner gate must be collected")
	// A plain RequirePermission owner gate (the regression) is NOT collected as a
	// RequirePermissionForResource triple → in production it would be a MISSING entry.
	assert.NotContains(t, collected, "redfixture|userID|PermRoleRead",
		"reverse fixture: a regression to plain auth.RequirePermission must NOT be collected as an owner gate (so production reports it MISSING)")
	// #1863: a well-formed RequirePermissionForSelf gate is collected as a "self" triple
	// (proves the self-branch extraction is exercised, not vacuous).
	assert.Contains(t, collected, "redfixture|self|PermSystemRead",
		"reverse fixture: a well-formed RequirePermissionForSelf gate must be collected as a self triple")
	// A self-gate regressed to plain RequirePermission is NOT collected as a self triple
	// → in production it would be a MISSING entry, identical to the RequirePermissionForResource regression.
	assert.NotContains(t, collected, "redfixture|self|PermConfigRead",
		"reverse fixture: a self-gate regressed to plain auth.RequirePermission must NOT be collected as a self triple")
	for k := range collected {
		assert.False(t, strings.Contains(k, "PermRoleRead"),
			"reverse fixture: the plain RequirePermission(PermRoleRead) gate must not produce any owner-gate triple, got %q", k)
	}
}
