//go:build archtest

// tenant_rls_read_caller_test.go — closes the READ side of the accesscore
// tenant-scope boundary (#1690), the future-drift counterpart to the scope-WRITE
// funnels.
//
//   - INVARIANT: TENANT-RLS-READ-CALLER-01
//
// # What this guards
//
// accesscore has three Postgres tables under FORCE ROW LEVEL SECURITY (users /
// roles / role_assignments, migration 053). A production read of any of them MUST
// run inside a transaction that wrote the app.tenant_id GUC (via scopedtx.Do /
// scopedtx.ApplyScope / a post-auth txRunner.RunInTx whose ctxkeys fallback sets
// the GUC) — a bare-pool read sees an UNSET GUC → tenant_id = NULL predicate →
// fail-closed 0 rows under the restricted app-serving pool (#1676). PR #1678
// finding F1 fixed the three sites that had drifted out of a scoped tx; today
// there are 0 residual sites. This rule is the FUTURE-DRIFT guard: it freezes the
// set of files that may reference an accesscore RLS-table read method, so a NEW
// reader fails CI until it is consciously reviewed (and, by that review, confirmed
// to run inside a scoped tx) and added to the allowlist with rationale.
//
// The scope-WRITE side is already funnel-locked (TENANT-TXSCOPE-WRITE-CALLER-01
// pins tenant.WithScope; TENANT-APPLYSCOPE-WRITE-CALLER-01 pins the mid-tx GUC
// write). This rule closes the symmetric read side with the same caller-allowlist
// mechanism.
//
// # Why a caller-allowlist, not a lexical body-gate
//
// accesscore reads pervasively run inside MULTI-LEVEL named *Tx helpers that
// receive the tx-context as a parameter, e.g. identitymanage:
//
//	RunInTx(ctx, func(txCtx){ return s.applyUserUpdateTx(ctx, txCtx, …) })
//	  → applyUserUpdateTx → guardUpdateStatusDemotion → checkLastAdminRemoval
//	  → s.lastAdminRoleRepo.GetByUserID(ctx, …)   // 4 frames below the wrapper
//
// A lexical "read is inside a scopedtx.Do/RunInTx closure body" gate would either
// false-positive on these helper-borne reads or require a fragile, drift-prone
// enrollment of every *Tx helper — the Soft pattern the AI-robust charter bans.
// The caller-allowlist is indirection-immune: it records WHICH FILE references a
// read method, regardless of nesting depth. (The compile-time-Hard alternative —
// a sealed ScopedCtx capability making an unscoped read unrepresentable — was
// assessed and deferred: it adds ZERO confidentiality over this guard because the
// runtime RLS fail-closed backstop catches an unscoped read in BOTH designs, while
// costing a cx-4 signature fan-out plus an un-typeable use-after-tx hole. See the
// follow-up issue.)
//
// # Detection + anti-vacuity
//
// Use-based (info.Uses → *types.Func), receiver-bound to ports.UserRepository /
// ports.RoleRepository (methodRecvTypeName). The receiver binding is what
// excludes the same-named collisions: policymanage's PolicyRepository.GetByID,
// the identitymanage *Service.GetByID handler method, and the mem/postgres
// concrete-repo self-calls (a different receiver type / package) all resolve to a
// non-matching owner and are skipped. The read-method set excludes WRITE methods
// (Create / Update* / Delete / Assign* …), which hit the same RLS tables but
// always run inside RunInTx already (writes are inherently transactional). The
// anti-vacuity reverse check requires every allowlist entry to reference a read
// method live, so a scanner regression or a removed reader (which would make the
// freeze vacuously pass) fails CI. The RED fixture (internal/rlsreadfixture,
// TestTenantRLSReadCaller01_FixtureCatchesRead) proves the detector fires on a
// genuine read reference and not on a write.
//
// # AI-robust rating (charter §"Funnel 双向锁评级") — MEDIUM
//
// Overall MEDIUM: a new unscoped read is expressible and COMPILES; the CI
// archtest is the enforcement — the same tier as the sibling scope-WRITE guards
// (a caller-allowlist, not a compile-time seal). The two axes:
//
//   - Downstream (detection robustness): alias-proof. Resolution is use-based
//     (info.Uses → *types.Func) + receiver-bound, so no import form (qualified /
//     alias / dot-import) and no same-name collision evades it. This matches what
//     the sibling guards label "HARD" on this axis — but it is robust DETECTION,
//     not a compile-time Hard tier: a non-allowlisted reference still compiles,
//     which is exactly what keeps the rule Medium.
//   - Upstream (can an RLS read happen WITHOUT referencing these methods):
//     MEDIUM. The ports repo interface methods are the sole sanctioned read path
//     for these tables, and repo reads route through the sealed ambient-tx
//     PGExecutor (PG-REPO-AMBIENT-TX-01), so a repo read honors the scoped tx.
//     Raw SQL issued OUTSIDE the repo methods is NOT covered by this rule (a
//     separate concern; no claim is made here that it is sealed).
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - File granularity: a SECOND unscoped read added inside an ALREADY-allowlisted
//     file is not caught (same limit as the sibling write-side guards). The runtime
//     RLS fail-closed (0 rows, adapters/postgres/tx_manager.go tenantScopeForTx /
//     setLocalTenant) is the correctness backstop.
//   - A read assembled by reflection or through an erased (non-ports) receiver does
//     not resolve to a ports interface — the same limit as every identifier-
//     resolution funnel in this suite.
//   - Raw SQL against users / roles / role_assignments outside the repo methods is
//     invisible here.
//   - A read reached only through a narrow domain facade interface (e.g.
//     domain.EffectiveAdminCounter.CountEffectiveAdmins, which RoleRepository
//     satisfies structurally) is referenced by the facade type, not ports, so it is
//     not matched — that is why internal/domain/admin.go is not in the allowlist.
//     Today the sole such facade's production caller chain
//     (identitymanage.checkLastAdminRemoval) runs inside a scoped tx, but that is a
//     CURRENT-STATE snapshot, NOT an invariant this rule guards: a new unscoped
//     caller of an RLS-reading facade method would not be caught here (the runtime
//     RLS fail-closed backstop still applies).
//
// # CI bucket
//
// Discovered + run by `gocell verify archtest` (whole-module packages.Load);
// routed to the NIGHTLY bucket, NOT the PR-time archtest-invariants gate (per the
// budget reasoning in hack/verify-archtest-invariants.sh). So a read-caller drift
// is caught at the next nightly run, not on the introducing PR.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

// accesscorePortsPkg (the package owning the UserRepository / RoleRepository
// interfaces whose READ methods touch the RLS tables) is declared in
// tenant_repo_param_funnel_test.go:104 (same package const) and reused here.

const (
	userRepositoryTypeName = "UserRepository"
	roleRepositoryTypeName = "RoleRepository"
)

// rlsReadMethodsByIface maps each RLS-reading repo interface to its READ method
// set. WRITE methods are deliberately excluded (see godoc §Detection). This set
// IS the protected-operation definition; the anti-vacuity check pins the
// load-bearing entries live so it cannot silently drift.
var rlsReadMethodsByIface = map[string]map[string]struct{}{
	userRepositoryTypeName: {
		"GetByIDInTenant":        {},
		"GetByUsername":          {},
		"GetByIDForUpdate":       {},
		"GetByUsernameForUpdate": {},
	},
	roleRepositoryTypeName: {
		"GetByID":              {},
		"GetByUserID":          {},
		"CountByRole":          {},
		"CountEffectiveAdmins": {},
		"EffectiveAdminExists": {},
		"ListByUserID":         {},
	},
}

// rlsReadCallerAllowlist: the files that legitimately reference an accesscore
// RLS-table read method (production slices, internal helpers, and the test-support
// conformance / fixture packages that exercise the repos). A NEW entry must be
// added consciously, after confirming the read runs inside a tenant-scoped tx.
var rlsReadCallerAllowlist = map[string]struct{}{
	"corecells/accesscore/slices/sessionlogin/service.go":         {},
	"corecells/accesscore/slices/sessionrefresh/service.go":       {},
	"corecells/accesscore/slices/sessionvalidate/service.go":      {},
	"corecells/accesscore/slices/rbacassign/service.go":           {},
	"corecells/accesscore/slices/rbaccheck/service.go":            {},
	"corecells/accesscore/slices/identitymanage/service.go":       {},
	"corecells/accesscore/internal/adminprovision/provisioner.go": {},
	"corecells/accesscore/internal/sessionmint/sessionmint.go":    {},
	// test-support readers (no scoped-tx obligation — they exercise the repos in
	// tests, not on a production request path):
	"corecells/accesscore/accesscoretest/fixture.go":                 {},
	"corecells/accesscore/internal/ports/conformance/conformance.go": {},
}

// rlsReadRef is one resolved reference to an RLS-table read method.
type rlsReadRef struct {
	rel    string
	line   int
	iface  string
	method string
}

// collectRLSReadRefs returns every reference, in p, to a read method (per
// ifaceReads) owned by an interface in portsPkg. Resolution is use-based
// (info.Uses) and receiver-bound (methodRecvTypeName), so it is invariant to
// import alias and matches both calls and method-value references. Parameterized
// by portsPkg/ifaceReads so the production scan and the RED fixture share one
// detector core.
func collectRLSReadRefs(p *Pass, portsPkg string, ifaceReads map[string]map[string]struct{}) []rlsReadRef {
	if p.TypesInfo == nil {
		return nil
	}
	var out []rlsReadRef
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != portsPkg {
				return
			}
			reads, ok := ifaceReads[methodRecvTypeName(fn)]
			if !ok {
				return
			}
			if _, isRead := reads[fn.Name()]; !isRead {
				return
			}
			out = append(out, rlsReadRef{
				rel:    rel,
				line:   p.Fset.Position(id.Pos()).Line,
				iface:  methodRecvTypeName(fn),
				method: fn.Name(),
			})
		})
	}
	return out
}

// TestTenantRLSReadCaller01 asserts every production reference to an accesscore
// RLS-table read method sits in rlsReadCallerAllowlist, and that every allowlist
// entry is live (anti-vacuity).
func TestTenantRLSReadCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil // detection is type-dependent (info.Uses); skip AST-only passes.
		}
		var d []Diagnostic
		for _, ref := range collectRLSReadRefs(p, accesscorePortsPkg, rlsReadMethodsByIface) {
			observed[ref.rel] = struct{}{}
			if _, allowed := rlsReadCallerAllowlist[ref.rel]; !allowed {
				// Message carries no ruleID prefix — Report prepends it (avoids a
				// double "TENANT-RLS-READ-CALLER-01:" in the output).
				d = append(d, Diagnostic{
					Rel:  ref.rel,
					Line: ref.line,
					Message: fmt.Sprintf(
						"%s.%s (a read of an RLS-protected table: users/roles/role_assignments, FORCE ROW LEVEL "+
							"SECURITY migration 053) is referenced from %s, which is not a sanctioned RLS reader. The "+
							"read MUST run inside a tenant-scoped tx (scopedtx.Do / scopedtx.ApplyScope / a post-auth "+
							"RunInTx whose ctxkeys fallback writes app.tenant_id); a bare-pool read fail-closes to 0 rows "+
							"under the restricted app-serving pool (#1676). If this IS a new sanctioned reader, confirm it "+
							"runs inside a scoped tx and add %s to rlsReadCallerAllowlist with rationale.",
						ref.iface, ref.method, ref.rel, ref.rel,
					),
				})
			}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry must
	// reference a read method live, else a scanner regression or a removed reader
	// would make the freeze vacuously pass.
	for f := range rlsReadCallerAllowlist {
		if _, seen := observed[f]; !seen {
			// Rel = the stale entry itself, so the diagnostic points at the file to
			// drop; Message carries no ruleID prefix (Report prepends it).
			diags = append(diags, Diagnostic{
				Rel: f,
				Message: fmt.Sprintf(
					"allowlist entry %q is STALE — no live accesscore RLS read-method reference observed. Either "+
						"the scanner regressed or the reader was removed; drop the dead allowlist entry so it cannot "+
						"become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "TENANT-RLS-READ-CALLER-01", diags)
}

// TestTenantRLSReadCaller01_FixtureCatchesRead is the reverse self-check: the RED
// fixture references a read method on a stand-in repository interface from a
// non-allowlisted file. The detector core (run against the fixture package) must
// resolve and flag exactly that ONE read reference — and must NOT flag the
// fixture's write reference. A 0 result means the detector regressed (lost the
// receiver binding or the read-method-set filter) and a new unscoped RLS read
// could be added unnoticed.
func TestTenantRLSReadCaller01_FixtureCatchesRead(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/rlsreadfixture"
	pattern := "./tools/archtest/internal/rlsreadfixture/..."
	fixtureReads := map[string]map[string]struct{}{
		userRepositoryTypeName: {"GetByIDInTenant": {}},
		roleRepositoryTypeName: {"CountEffectiveAdmins": {}},
	}
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg {
			return nil
		}
		var d []Diagnostic
		for _, ref := range collectRLSReadRefs(p, fixturePkg, fixtureReads) {
			d = append(d, Diagnostic{Rel: ref.rel, Line: ref.line, Message: ref.iface + "." + ref.method + " reference"})
		}
		return d
	})
	for _, dd := range diags {
		t.Log(dd.Message)
	}
	require.Len(t, diags, 2,
		"the detector must resolve BOTH out-of-allowlist read references (UserRepository + RoleRepository) and "+
			"exclude the write; a wrong count means receiver-binding or the read-method-set filter regressed and an "+
			"unscoped RLS read could be added unnoticed")
}
