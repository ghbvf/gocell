//go:build archtest

// tenant_rls_read_caller_test.go — closes the READ side of the per-cell
// tenant-scope boundary across enrolled corecells (#1690, #2392), the
// future-drift counterpart to the scope-WRITE funnels.
//
//   - INVARIANT: TENANT-RLS-READ-CALLER-01
//
// # What this guards
//
// Several corecells own Postgres tables under FORCE ROW LEVEL SECURITY. A
// production read of such a table MUST run inside a transaction that wrote the
// app.tenant_id GUC (via scopedtx.Do / scopedread.Do / a post-auth
// txRunner.RunInTx whose ctxkeys fallback sets the GUC) — a bare-pool read sees
// an UNSET GUC → tenant_id = NULL predicate → fail-closed 0 rows under the
// restricted app-serving pool (#1676). This rule is the FUTURE-DRIFT guard: per
// enrolled cell it freezes the set of files that may reference an RLS-table read
// method DIRECTLY, so a new such reference fails CI until it is consciously
// reviewed (and, by that review, confirmed to run inside a scoped tx) and added
// to that cell's allowlist with rationale. A read reached only THROUGH A FACADE
// METHOD (which references the facade type, not a ports read method) is NOT
// caught here — see §"Tool blind spots"; the runtime RLS fail-closed (0 rows) is
// the correctness backstop for it.
//
// # Enrolled cells (rlsReadCells)
//
//   - accesscore — users / roles / role_assignments (migration 053); reads on
//     UserRepository / RoleRepository. PR #1678 finding F1 fixed the three sites
//     that had drifted out of a scoped tx; today there are 0 residual sites.
//   - registrycore — contract_registrations / contract_registration_events
//     (migration 066, #2392); reads on Registry (Get / List / History). The sole
//     production reader, registryread.List, wraps the read in scopedread.Do.
//
// configcore is the SAME pattern (FORCE RLS migration 052 + its own scopedread
// funnel over ConfigRepository / FlagRepository) and is already a
// TENANT-REPO-PARAM-FUNNEL-01 enrolled cell, but is NOT yet enrolled HERE —
// deferred to #2450 (enrolling it requires classifying its read/write method sets
// and confirming its read callers run scoped). Until then configcore reads rely
// on the runtime RLS fail-closed backstop, same as the facade blind spots below.
//
// The scope-WRITE side is already funnel-locked (TENANT-TXSCOPE-WRITE-CALLER-01
// pins tenant.WithScope; TENANT-APPLYSCOPE-WRITE-CALLER-01 pins the mid-tx GUC
// write). This rule closes the symmetric read side with the same caller-allowlist
// mechanism.
//
// # Why a caller-allowlist, not a lexical body-gate
//
// These reads pervasively run inside MULTI-LEVEL named *Tx helpers that receive
// the tx-context as a parameter, e.g. accesscore identitymanage:
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
// assessed and deferred to #1893: it adds ZERO confidentiality over this guard
// because the runtime RLS fail-closed backstop catches an unscoped read in BOTH
// designs, while costing a cx-4 signature fan-out plus an un-typeable
// use-after-tx hole.)
//
// # Detection + anti-vacuity
//
// Use-based (info.Uses → *types.Func), receiver-bound to the cell's ports repo
// interfaces (methodRecvTypeName). The receiver binding is what excludes the
// same-named collisions: policymanage's PolicyRepository.GetByID, the
// identitymanage *Service.GetByID handler method, and the mem/postgres
// concrete-repo self-calls (a different receiver type / package) all resolve to a
// non-matching owner and are skipped. The protected read set is DERIVED per cell,
// not hand-listed: it is the live interface method set MINUS the WRITE methods
// (spec.writeMethods — Create / Update* / Delete / Assign* / Transition …), which
// hit the same RLS tables but always run inside RunInTx already (writes are
// inherently transactional). Deriving from the interface makes read COMPLETENESS
// structural — a new RLS read method is protected by default, and the
// classification guard (TestTenantRLSReadCaller01_ReadMethodClassification) fails
// CI if a new method escapes both sets, a write entry goes stale, or an interface
// starts embedding. The anti-vacuity reverse check requires every allowlist entry
// (across all enrolled cells) to reference a read method live, so a scanner
// regression or a removed reader (which would make the freeze vacuously pass)
// fails CI. The RED fixture (internal/rlsreadfixture,
// TestTenantRLSReadCaller01_FixtureCatchesRead) proves the detector core fires on
// a genuine read reference and not on a write.
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
//   - The SAME facade limit applies to adminprovision.Provisioner.Status / Ensure,
//     which wrap roleRepo.EffectiveAdminExists / CountByRole: provisioner.go IS in
//     the allowlist (it references the ports read methods directly), but the
//     Provisioner's callers reference Provisioner.Status / Ensure — not a ports read
//     method — so a NEW unscoped caller of those facade methods is not caught here.
//     Today the sole production caller (slices/setup/service.go Status / CreateAdmin,
//     a pre-auth path with no JWT tenant fallback) wraps every call in scopedtx.Do
//     — again a CURRENT-STATE snapshot, not an invariant this rule guards. The
//     principled static closure for BOTH facade cases is the sealed ScopedCtx
//     capability deferred to #1893 (it would make an unscoped read uncompilable
//     through ANY path, facade or direct); until then the runtime RLS fail-closed
//     backstop is the correctness boundary.
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

// accesscorePortsPkg / registrycorePortsPkg (the packages owning the repo
// interfaces whose READ methods touch the RLS tables) are declared in
// tenant_repo_param_funnel_test.go (same package consts) and reused here.

const (
	userRepositoryTypeName     = "UserRepository"
	roleRepositoryTypeName     = "RoleRepository"
	registryRepositoryTypeName = "Registry"
)

// rlsReadCellSpec enrolls one cell's RLS-table repo interfaces into this guard.
// Per spec the protected READ set is DERIVED as (live interface method set −
// writeMethods), so read completeness is STRUCTURAL per cell: a method newly
// added to an enrolled interface is, by default, a protected read this rule
// catches (the anti-vacuity check only proves listed readers stay live, not that
// the list is complete — deriveRLSReadMethods gives the completeness half).
type rlsReadCellSpec struct {
	// name labels the cell in diagnostics.
	name string
	// portsPkg is the full import path of the package OWNING the repo interfaces
	// whose read methods touch the RLS tables (matched against fn.Pkg().Path()).
	portsPkg string
	// portsDirRel is portsPkg's repo-relative dir, for the AST interface load.
	portsDirRel string
	// ifaces are the repo interface names in portsPkg whose EVERY method touches
	// an RLS-protected table (each method is either a read or a write).
	ifaces []string
	// writeMethods is the SOLE hand-maintained classification: per interface, the
	// WRITE methods. Writes hit the same RLS tables but are inherently
	// transactional (always inside RunInTx), so the read-side allowlist does not
	// freeze them; reads = live method set − this set. A NEW write method must be
	// listed here, else it is (mis)derived as a protected read and flags at its
	// call sites — fail-closed toward protection, by design.
	writeMethods map[string]map[string]struct{}
	// allowlist is the set of files that may reference a read method of this
	// cell's ifaces. A NEW entry must be added consciously, after confirming the
	// read runs inside a tenant-scoped tx.
	allowlist map[string]struct{}
}

// rlsReadCells is the closed set of cells enrolled in TENANT-RLS-READ-CALLER-01.
// accesscore (users/roles/role_assignments, migration 053) and registrycore
// (contract_registrations/contract_registration_events, migration 066, #2392)
// are covered; configcore is the SAME pattern (FORCE RLS migration 052 + its own
// scopedread funnel) but is NOT yet enrolled — tracked by #2450.
var rlsReadCells = []rlsReadCellSpec{
	{
		name:        "accesscore",
		portsPkg:    accesscorePortsPkg,
		portsDirRel: "corecells/accesscore/internal/ports",
		ifaces:      []string{userRepositoryTypeName, roleRepositoryTypeName},
		writeMethods: map[string]map[string]struct{}{
			userRepositoryTypeName: {
				"Create":                  {},
				"Delete":                  {},
				"UpdateProfile":           {},
				"UpdateLockState":         {},
				"UpdatePasswordResetFlag": {},
				"UpdatePassword":          {},
				"BumpAuthzEpoch":          {},
				"UpdateLockoutFields":     {},
			},
			roleRepositoryTypeName: {
				"Create":                  {},
				"AssignToUser":            {},
				"RemoveFromUser":          {},
				"RemoveFromUserIfNotLast": {},
			},
		},
		// Production slices, internal helpers, and the test-support conformance /
		// fixture packages that exercise the repos.
		allowlist: map[string]struct{}{
			"corecells/accesscore/slices/sessionlogin/service.go":         {},
			"corecells/accesscore/slices/sessionrefresh/service.go":       {},
			"corecells/accesscore/slices/sessionvalidate/service.go":      {},
			"corecells/accesscore/slices/rbacassign/service.go":           {},
			"corecells/accesscore/slices/rbaccheck/service.go":            {},
			"corecells/accesscore/slices/identitymanage/service.go":       {},
			"corecells/accesscore/internal/adminprovision/provisioner.go": {},
			"corecells/accesscore/internal/sessionmint/sessionmint.go":    {},
			// test-support readers (no scoped-tx obligation — they exercise the
			// repos in tests, not on a production request path):
			"corecells/accesscore/accesscoretest/fixture.go":                 {},
			"corecells/accesscore/internal/ports/conformance/conformance.go": {},
		},
	},
	{
		name:        "registrycore",
		portsPkg:    registrycorePortsPkg,
		portsDirRel: "corecells/registrycore/internal/ports",
		ifaces:      []string{registryRepositoryTypeName},
		writeMethods: map[string]map[string]struct{}{
			// Registry.Create/Transition write the projection + append-only history
			// (L1); Get/List/History are the protected reads (#2392).
			registryRepositoryTypeName: {
				"Create":     {},
				"Transition": {},
			},
		},
		// registryread.List is the sole production reader; it wraps the read in
		// scopedread.Do (tenant.WithScope + RunInTx) — see #2392.
		allowlist: map[string]struct{}{
			"corecells/registrycore/slices/registryread/service.go": {},
		},
	},
}

// deriveRLSReadMethods loads spec's live interface method sets (AST) and returns
// (iface → read-method set) = full − spec.writeMethods[iface]. It fails t on any
// classification drift: an interface that embeds a sub-interface (promoted
// methods would escape the AST scan), a write-exclusion entry that is not a live
// interface method (typo / stale — would shrink the write set and misclassify a
// real write as a read), or an interface whose derived read set is empty (the
// whole interface classified as writes — almost certainly a mistake that would
// make the rule vacuous for it). This is the completeness half the anti-vacuity
// check cannot give: anti-vacuity proves every observed read has a live caller;
// this pins the read SET to the interface fact, per enrolled cell.
func deriveRLSReadMethods(t *testing.T, root string, spec rlsReadCellSpec) map[string]map[string]struct{} {
	t.Helper()
	reads := make(map[string]map[string]struct{}, len(spec.ifaces))
	for _, ifaceName := range spec.ifaces {
		iface := loadInterfaceType(t, root, spec.portsDirRel, ifaceName)
		if iface == nil {
			t.Fatalf("TENANT-RLS-READ-CALLER-01: %s not found in %s (cell %s)", ifaceName, spec.portsDirRel, spec.name)
		}
		if embedded := embeddedTypeNames(iface); len(embedded) > 0 {
			t.Errorf("TENANT-RLS-READ-CALLER-01: %s must not embed sub-interfaces "+
				"(promoted methods would escape the read-method derivation); got %v", ifaceName, embedded)
		}
		methods := directMethodNames(iface)
		methodSet := make(map[string]struct{}, len(methods))
		for _, m := range methods {
			methodSet[m] = struct{}{}
		}
		writes := spec.writeMethods[ifaceName]
		for w := range writes {
			if _, ok := methodSet[w]; !ok {
				t.Errorf("TENANT-RLS-READ-CALLER-01: write-exclusion %q is not a method of %s "+
					"(typo or stale entry); fix the %s spec writeMethods", w, ifaceName, spec.name)
			}
		}
		read := make(map[string]struct{}, len(methods))
		for _, m := range methods {
			if _, isWrite := writes[m]; !isWrite {
				read[m] = struct{}{}
			}
		}
		if len(read) == 0 {
			t.Errorf("TENANT-RLS-READ-CALLER-01: derived read set for %s is empty "+
				"(every method classified as a write); the rule would be vacuous for this interface", ifaceName)
		}
		reads[ifaceName] = read
	}
	return reads
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

// TestTenantRLSReadCaller01 asserts every production reference to an enrolled
// cell's RLS-table read method sits in that cell's allowlist, and that every
// allowlist entry (across all enrolled cells) is live (anti-vacuity).
func TestTenantRLSReadCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)

	// Per cell: derive the protected read set from the live interface fact
	// (completeness is structural, not a hand-list) and index its allowlist for
	// the anti-vacuity reverse check. Allowlist files are disjoint across cells
	// (each lives under its own cell dir), so a single observed set is sound.
	type resolvedCell struct {
		spec  rlsReadCellSpec
		reads map[string]map[string]struct{}
	}
	cells := make([]resolvedCell, 0, len(rlsReadCells))
	allowToCell := map[string]string{}
	for _, spec := range rlsReadCells {
		cells = append(cells, resolvedCell{spec: spec, reads: deriveRLSReadMethods(t, root, spec)})
		for f := range spec.allowlist {
			allowToCell[f] = spec.name
		}
	}

	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil // detection is type-dependent (info.Uses); skip AST-only passes.
		}
		var d []Diagnostic
		for _, c := range cells {
			for _, ref := range collectRLSReadRefs(p, c.spec.portsPkg, c.reads) {
				observed[ref.rel] = struct{}{}
				if _, allowed := c.spec.allowlist[ref.rel]; !allowed {
					// Message carries no ruleID prefix — Report prepends it (avoids a
					// double "TENANT-RLS-READ-CALLER-01:" in the output).
					d = append(d, Diagnostic{
						Rel:  ref.rel,
						Line: ref.line,
						Message: fmt.Sprintf(
							"%s.%s (a read of a %s RLS-protected table under FORCE ROW LEVEL SECURITY) is referenced "+
								"from %s, which is not a sanctioned RLS reader. The read MUST run inside a tenant-scoped "+
								"tx (scopedtx.Do / scopedread.Do / a post-auth RunInTx whose ctxkeys fallback writes "+
								"app.tenant_id); a bare-pool read fail-closes to 0 rows under the restricted app-serving "+
								"pool. If this IS a new sanctioned reader, confirm it runs inside a scoped tx and add %s "+
								"to the %s cell allowlist in rlsReadCells with rationale.",
							ref.iface, ref.method, c.spec.name, ref.rel, ref.rel, c.spec.name,
						),
					})
				}
			}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlist entry (across
	// all enrolled cells) must reference a read method live, else a scanner
	// regression or a removed reader would make the freeze vacuously pass.
	for f, cellName := range allowToCell {
		if _, seen := observed[f]; !seen {
			// Rel = the stale entry itself, so the diagnostic points at the file to
			// drop; Message carries no ruleID prefix (Report prepends it).
			diags = append(diags, Diagnostic{
				Rel: f,
				Message: fmt.Sprintf(
					"allowlist entry %q (%s) is STALE — no live RLS read-method reference observed. Either the "+
						"scanner regressed or the reader was removed; drop the dead allowlist entry so it cannot "+
						"become a silent bypass slot.",
					f, cellName,
				),
			})
		}
	}

	Report(t, "TENANT-RLS-READ-CALLER-01", diags)
}

// TestTenantRLSReadCaller01_ReadMethodClassification pins the read/write
// classification to the LIVE interface fact for every enrolled cell (AST-only, so
// it runs even in -short mode, unlike the packages.Load production scan above). It
// is the completeness guard: adding a read method to an enrolled interface without
// listing it as a write in the cell's spec.writeMethods makes it a derived
// protected read; a stale or misspelled write entry, an emptied read set, or a
// newly embedded sub-interface fails here. deriveRLSReadMethods carries the
// assertions.
func TestTenantRLSReadCaller01_ReadMethodClassification(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	for _, spec := range rlsReadCells {
		_ = deriveRLSReadMethods(t, root, spec)
	}
}

// TestTenantRLSReadCaller01_FixtureCatchesRead is the reverse self-check: the RED
// fixture references a read method on EACH of two stand-in repository interfaces
// (UserRepository + RoleRepository) from a non-allowlisted file. The detector core
// (run against the fixture package) must resolve and flag exactly those TWO read
// references — one per interface, proving the receiver binding resolves both — and
// must NOT flag the fixture's write reference. A wrong count means the detector
// regressed (lost the receiver binding or the read-method-set filter) and a new
// unscoped RLS read could be added unnoticed.
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
