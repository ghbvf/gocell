// invariants:
//   - INVARIANT: PG-REPO-AMBIENT-TX-01
//
// # Package archtest — PG-REPO-AMBIENT-TX-01
//
// PG-REPO-AMBIENT-TX-01 enforces the ambient-tx routing contract for
// PostgreSQL-backed repositories. The funnel is sealed at the Go visibility
// level: the sanctioned holder type pgExecutor lives in a per-adapter
// /internal/pgexec/ sub-package and is unexported there; parent packages can
// only obtain a value via pgexec.New(pool) and can only invoke methods through
// the exported pgexec.PGExecutor interface, which does not expose the raw
// *pgxpool.Pool field. The interface itself is SEALED via an unexported marker
// method (see InterfaceSealed below) so a parent package cannot declare a
// parallel implementation.
//
//   - R1 (no raw pool in repo/store layer): every *_repo.go / *_store.go file
//     must not declare a *pgxpool.Pool field. Pool access is funneled through
//     the exported pgexec.PGExecutor interface (held as a field).
//     Infrastructure files (pool.go, tx_manager.go) and the sub-package's own
//     pgexec.go legitimately hold the raw pool and are out of scope by
//     file-extension filter — a PRE-EXISTING Soft scope boundary (#1206 tracks
//     the full pool-holder seal that would remove it).
//
//   - R2 (cross-package wrap funnel): every *_repo.go / *_store.go file's
//     function parameter of type *pgxpool.Pool must satisfy BOTH (a) function
//     name starts with "New" AND (b) function body calls pgexec.New(param)
//     resolved via *types.Info to a *types.Func with Pkg().Path() ending
//     /internal/pgexec AND Name() == "New". Unnamed pool params are counted
//     too (an unnamed New* param cannot be referenced, hence cannot be wrapped,
//     so it is flagged). Same PRE-EXISTING file-extension Soft scope as R1.
//
//   - R3 (ExecDirect call-bound approval, GLOBAL scope): every CallExpr
//     resolving to pgexec.ExecDirect (callee identity via *types.Info →
//     *types.Func with Pkg().Path() ending /internal/pgexec AND Name() ==
//     "ExecDirect") MUST pass, as its FIRST argument, an inline CallExpr to
//     pgrepoapproved.Approve whose own first argument is a kebab-case string
//     literal (^[a-z][a-z0-9-]+$, non-placeholder). R3 scans ALL production
//     files regardless of filename — ExecDirect is rare and identified by
//     callee identity, so no file-extension scope is needed (unlike R1/R2).
//
// # AI-robust grading (Funnel 双向锁评级, amended 2026-05-28)
//
// **上游 — pgExecutor 形态包外不可达**: **Hard** (compile-time, Go visibility).
// pgExecutor struct + *pgxpool.Pool field unexported in per-adapter
// internal/pgexec/ sub-package — sealed construction (ai-robust.md §Hard 范本 #6).
//
// **上游 — PGExecutor interface 包外不可实现**: **Hard** (Go visibility, sealed
// interface) + **archtest regression backstop** (InterfaceSealed). The
// unexported sealPGExecutor marker method means external packages cannot
// declare a parallel PGExecutor; TestPGRepoAmbientTx_InterfaceSealed asserts
// the marker stays present (exactly one unexported interface method) and that
// *pgExecutor implements it, so the seal cannot be silently removed by a later
// edit (sibling form to DETAILS-SEALED-FIELD-FROZEN-01 / ListenerAuth marker).
//
// **下游 R1 / R2 — pool field + wrap funnel**: archtest via *types.Info global
// predicate, scoped by *_repo.go / *_store.go file extension — PRE-EXISTING
// Soft scope (PR #917), upgrade tracked by #1206 (PG-INFRA-FULL-SEAL-01). The
// compile-time Hard comes from the sub-pkg seal; archtest is defense-in-depth.
//
// **下游 R3 — ExecDirect callsite**: **Hard via callee identity + call-bound
// approval**. ExecDirect is a top-level function pgexec.ExecDirect(approval, e,
// ctx, sql, args...), NOT a method — subset-interface bypass is closed at the
// type level. The approval token is the first argument: a bypass cannot exist
// without an inline pgrepoapproved.Approve("<kebab-literal>) (call-bound, no
// scope co-location, no marker reuse, no nested-closure smuggling). Form-
// uniqueness on the Approve arg (BasicLit + token.STRING + kebab regex +
// non-placeholder) mirrors panicregister (ai-robust.md §Hard 范本 #2).
//
// # RED fixtures
//
// The authoritative expected-violation MULTISET is expectedFixtureViolations,
// asserted by TestPGRepoAmbientTx_RedFixtureDetected with exact per-key counts.
// The fixture package at tools/archtest/internal/pgrepoambienttxfixture/:
//
//   - fixture_repo.go: R1 + R2 (incl. unnamed-param cases) + R3 call-bound RED
//     forms + GREEN controls.
//   - fixture_service.go: a NON-_repo.go file holding an R3 RED case — proves
//     R3's global scope (R1/R2 would exempt this filename; R3 must not).
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring production form.
//
// # Blind spots
//
// BS-1 Field embedding: an embedded struct transitively carrying *pgxpool.Pool
// — covered by TestPGRepoAmbientTx_SelfCheck via *types.Info.
//
// BS-2 Interface-typed field carrying *pgxpool.Pool at runtime — R1 is a
// static-type check; accepted per ai-robust.md §3.
//
// BS-3 Function-value indirection of pgexec.New (`var fn = pgexec.New;
// fn(pool)`) — reverse check asserts no function-value reference to pgexec.New.
//
// BS-4 *pgxpool.Pool type alias — resolves to the same *types.Named; documented.
//
// BS-5 cells/accesscore PGBundle helper accepts *pgxpool.Pool as a param but
// does not persist it — reverse check asserts no struct field outside internal/.
//
// BS-6 Function-value indirection of pgexec.ExecDirect (`var fn =
// pgexec.ExecDirect; fn(approval, ...)`) — R3 resolves the direct CallExpr
// callee, so a function-value call would escape it; reverse check asserts no
// function-value reference to pgexec.ExecDirect in production.
//
// BS-7 Function-value indirection of pgrepoapproved.Approve (`f := Approve;
// ExecDirect(f("x"), ...)`) — NOT a separate reverse check: R3's
// execDirectHasInlineApproval requires arg[0] to be a direct CallExpr whose
// callee resolves (via *types.Info) to pgrepoapproved.Approve. `f("x")` has
// callee Ident `f` resolving to a *types.Var (not the *types.Func), so
// resolveCalleeFunc returns nil and R3 flags it — caught by the main rule, no
// reverse check needed.
//
// (BS-8 spurious-marker retired: there is no standalone marker statement under
// the call-bound form — an approval cannot exist without an ExecDirect call.)
package archtest

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// fixtureViolation is a (base, ruleID_prefix, line) triple identifying one
// expected RED fixture diagnostic. base is the file basename (filepath.Base of
// the diagnostic's module-relative path): including it distinguishes
// same-line diagnostics across fixture files (F2 — a {rulePrefix, line}-only
// key collapses cross-file collisions and is blind to which file a regression
// landed in).
type fixtureViolation struct {
	base       string
	rulePrefix string
	line       int
}

// expectedFixtureViolations is the authoritative expected MULTISET for
// TestPGRepoAmbientTx_RedFixtureDetected. Lines are pinned to the fixture
// sources. When the fixture changes intentionally, update both the fixture
// and this set together. The oracle compares exact per-key counts (F2), so a
// duplicate diagnostic (same base+prefix+line emitted twice) is a mismatch
// unless listed twice here.
//
// File layout:
//   - fixture.go: package godoc only (no rules apply — not _repo.go)
//   - fixture_repo.go: R1 + R2 RED (file-extension scoped) + R3 RED + GREEN controls
//   - fixture_service.go: R3 RED in a NON-_repo.go file (proves R3 global scope)
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring production form
var expectedFixtureViolations = []fixtureViolation{
	// R1 RED (fixture_repo.go) — pointer to field type pos.
	{"fixture_repo.go", "R1:", 21}, // badR1Repo.pool
	// R2 RED (fixture_repo.go) — pointer to FuncDecl name pos.
	{"fixture_repo.go", "R2:", 31}, // badR2NonNew (named param)
	{"fixture_repo.go", "R2:", 37}, // NewBadR2NoWrap (named param, no pgexec.New)
	{"fixture_repo.go", "R2:", 44}, // badR2Unnamed (F1: unnamed non-New param)
	{"fixture_repo.go", "R2:", 49}, // NewBadR2Unnamed (F1: unnamed New param, cannot wrap)
	// R3 RED (fixture_repo.go) — pointer to pgexec.ExecDirect call pos.
	// Call-bound approval: arg[0] must be inline Approve(<catalog const>).
	{"fixture_repo.go", "R3:", 69}, // badR3LocalConst (locally-declared ApprovalReason const)
	{"fixture_repo.go", "R3:", 76}, // badR3TypeConversion (ApprovalReason("...") type conversion)
	{"fixture_repo.go", "R3:", 84}, // badR3ReusedApproval (arg[0] is *ast.Ident, not inline CallExpr)
	// R3 RED (fixture_service.go) — NON-_repo.go file; proves R3 global scope.
	{"fixture_service.go", "R3:", 21}, // serviceLayerBadExecDirect (reused approval, non-repo file)
	// R4 RED (fixture_repo.go) — pointer to pgrepoapproved.Approve call pos.
	// Orphan Approve: every Approve callsite must be Args[0] of ExecDirect.
	{"fixture_repo.go", "R4:", 83},  // badR3ReusedApproval's Approve is not inline at ExecDirect
	{"fixture_repo.go", "R4:", 95},  // badR4OrphanDiscarded (Approve assigned to blank)
	{"fixture_repo.go", "R4:", 102}, // badR4OrphanAssigned (Approve assigned to local var)
	// R4 RED (fixture_service.go) — non-_repo.go file proving R4 global scope.
	{"fixture_service.go", "R4:", 20}, // serviceLayerBadExecDirect's Approve is not inline
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01 against the production
// module. R1/R2 are file-extension-scoped global predicates; R3 is a global
// predicate over all production files. RED fixtures are exercised by
// TestPGRepoAmbientTx_RedFixtureDetected; the interface seal regression guard
// is TestPGRepoAmbientTx_InterfaceSealed; blind-spot self-checks are in
// TestPGRepoAmbientTx_SelfCheck.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	Report(t, "PG-REPO-AMBIENT-TX-01", CheckPGRepoAmbientTx(t, ConfigForExternalCell{}))
}

// TestPGRepoAmbientTx_RedFixtureDetected asserts the rule catches every RED
// violation in tools/archtest/internal/pgrepoambienttxfixture. The assertion
// is an exact MULTISET match on (base, ruleID_prefix, line) triples — counts
// must match exactly (F2), so duplicate or cross-file collisions are caught.
func TestPGRepoAmbientTx_RedFixtureDetected(t *testing.T) {
	t.Parallel()

	diags := Run(
		t, Fixture(

			FixtureOpts{Tests: false},
			[]string{"./tools/archtest/internal/pgrepoambienttxfixture/..."},
		),

		pgRepoAmbientTxRule,
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	type diagKey struct {
		base       string
		rulePrefix string
		line       int
	}
	actualCounts := make(map[diagKey]int, len(diags))
	for _, d := range diags {
		var prefix string
		switch {
		case strings.HasPrefix(d.Message, "R1:"):
			prefix = "R1:"
		case strings.HasPrefix(d.Message, "R2:"):
			prefix = "R2:"
		case strings.HasPrefix(d.Message, "R3:"):
			prefix = "R3:"
		case strings.HasPrefix(d.Message, "R4:"):
			prefix = "R4:"
		default:
			t.Errorf("unexpected diagnostic with unrecognized rulePrefix at %s:%d: %s",
				d.Rel, d.Line, d.Message)
			continue
		}
		actualCounts[diagKey{filepath.Base(d.Rel), prefix, d.Line}]++
	}

	expectedCounts := make(map[diagKey]int, len(expectedFixtureViolations))
	for _, v := range expectedFixtureViolations {
		expectedCounts[diagKey(v)]++
	}

	for k, want := range expectedCounts {
		if got := actualCounts[k]; got != want {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: %s %s:%d expected %d diagnostic(s) "+
				"but got %d — rule may have regressed or fixture line shifted; "+
				"update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.base, k.line, want, got)
		}
	}
	for k, got := range actualCounts {
		if want := expectedCounts[k]; want != got {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: %s %s:%d produced %d diagnostic(s) "+
				"not matched by expectedFixtureViolations (want %d) — GREEN cases must "+
				"produce 0; update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.base, k.line, got, want)
		}
	}
}

// TestPGRepoAmbientTx_InterfaceSealed is the regression backstop for the
// PGExecutor interface seal (upstream Hard). The seal property — external
// packages cannot implement PGExecutor — comes from an unexported marker
// method (sealPGExecutor). Go visibility makes that Hard against external
// implementers, but cannot prevent a later edit from deleting the marker
// method (the interface would still compile, all impls still work). This test
// asserts, for every production pgexec sub-package, that PGExecutor has
// EXACTLY ONE unexported method (the marker — structural, not name-anchored)
// and that *pgExecutor implements PGExecutor. Sibling form to
// DETAILS-SEALED-FIELD-FROZEN-01 (publicValue marker) / ListenerAuth marker.
func TestPGRepoAmbientTx_InterfaceSealed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Dynamic discovery: scan the whole production module and check EVERY
	// package whose import path ends /internal/pgexec. A new PG adapter
	// sub-package is covered automatically — no hand-maintained allowlist (which
	// would be the same Soft scope this funnel exists to remove). The sanity
	// anchor below guards the only failure mode dynamic discovery introduces:
	// silently finding zero pgexec packages (e.g. prodscan regression) → the
	// seal guard would vacuously pass.
	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)
	const anchorPkg = PlatformModulePath + "/adapters/postgres/internal/pgexec"

	var checked []string
	_ = Run(t, Typed(TypedOpts{}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || !isPgexecSubpackage(p.Pkg.Path()) {
			return nil
		}
		checked = append(checked, p.Pkg.Path())
		assertSealedInterface(t, p.Pkg, pgExecutorInterfaceName, pgExecutorImplName)
		return nil
	})

	assert.Contains(t, checked, anchorPkg,
		"InterfaceSealed: prodscan discovery did not find the canonical "+
			anchorPkg+" — discovery may have regressed; without it the seal "+
			"regression guard would vacuously pass")
}

// TestPGRepoApprovedSealed is the symmetric regression backstop for the
// pgrepoapproved.Approval token interface. R3 is the primary gate (arg[0] must
// be an inline Approve(literal) CallExpr), but the sealed Approval interface is
// defense-in-depth: if its unexported marker method were deleted, Approval
// would collapse to interface{} and a forged value could be passed as the
// approval token. This asserts the seal stays intact.
func TestPGRepoApprovedSealed(t *testing.T) {
	t.Parallel()
	Report(t, "PG-REPO-APPROVED-SEALED", CheckPGRepoApprovedSealed(t, ConfigForExternalCell{}))
}

// TestPGRepoAmbientTx_SelfCheck verifies the blind-spot list by asserting
// prohibited AST forms are absent from production packages.
func TestPGRepoAmbientTx_SelfCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SelfCheck in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)

	// BS-1: no embedded/anonymous struct field in any production _repo.go /
	// _store.go file should transitively expose *pgxpool.Pool.
	var bs1Violations []string
	_ = Run(t, Typed(TypedOpts{}, patterns), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			if !isRepoOrStoreFile(rel) {
				continue
			}
			EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					for _, field := range st.Fields.List {
						if len(field.Names) != 0 {
							continue
						}
						if embeddedStructHasPoolField(field.Type, p.TypesInfo) {
							bs1Violations = append(bs1Violations, fmt.Sprintf(
								"%s:%d: struct %s has embedded field that transitively "+
									"holds *pgxpool.Pool (BS-1 blind spot — extend R1 if needed)",
								rel, p.Fset.Position(field.Type.Pos()).Line, ts.Name.Name,
							))
						}
					}
				})
			})
		}
		return nil
	})

	assert.Empty(t, bs1Violations,
		"BS-1 self-check: no production repo/store struct may use an embedded field "+
			"that carries *pgxpool.Pool")

	// BS-3: pgexec.New must not appear as a function value (used as a value
	// rather than directly called) in any production package.
	var bs3Violations []string
	_ = Run(t, Typed(TypedOpts{}, patterns), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			bs3Violations = append(bs3Violations,
				findPgexecFuncValueUses(p.Fset, file, rel, p.TypesInfo, pgexecFactoryName)...)
		}
		return nil
	})

	assert.Empty(t, bs3Violations,
		"BS-3 self-check: pgexec.New must not be used as a function value in production")

	// BS-5: no struct in cells/accesscore (outside internal/) may carry
	// *pgxpool.Pool as a field. NewPGBundle accepts pool as a parameter but
	// does not persist it.
	bs5Patterns := []string{PlatformModulePath + "/cells/accesscore"}
	var bs5PoolFieldCount int
	_ = Run(t, Typed(TypedOpts{}, bs5Patterns), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					for _, field := range st.Fields.List {
						if isPgxPoolType(field.Type, p.TypesInfo) {
							bs5PoolFieldCount++
						}
					}
				})
			})
		}
		return nil
	})

	assert.Equal(t, 0, bs5PoolFieldCount,
		"BS-5 self-check: no struct in cells/accesscore (outside internal/) may carry "+
			"*pgxpool.Pool — PGBundle must hold only derived primitives "+
			"(userRepo/roleRepo/setupLock/txRunner)")

	// BS-6: pgexec.ExecDirect must not appear as a function value (used as a
	// value rather than directly called) in any production file. A function-
	// value call would escape R3's direct-CallExpr callee resolution.
	var bs6Violations []string
	_ = Run(t, Typed(TypedOpts{}, patterns), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			bs6Violations = append(bs6Violations,
				findPgexecFuncValueUses(p.Fset, file, rel, p.TypesInfo, execDirectName)...)
		}
		return nil
	})

	assert.Empty(t, bs6Violations,
		"BS-6 self-check: pgexec.ExecDirect must not be used as a function value in production")
}
