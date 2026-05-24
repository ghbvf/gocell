// INVARIANT: OPS-CONTRACT-STRING-FUNNEL-01
//
// ops_contract_string_funnel_test.go — OPS-CONTRACT-STRING-FUNNEL-01
//
// Rule: every adapter dependency-availability readiness-probe name
// (postgres_ready, redis_ready, s3_ready, rabbitmq_ready, vault_transit_ready,
// oidc_ready, websocket_hub_ready, postgres_indexes_valid_ready) must be
// authored as a kernel/healthz.ReadyProbeName-typed const declared at the
// owning adapter's own package, and referenced at the construction site
// (Checkers() map[string]func key via string(<const>), or
// adapterutil.HealthToCheckers first arg) by an identifier that resolves —
// through go/types info.Uses — to one of those declared consts. A bare string
// literal, a concat, a fmt.Sprintf, or a runtime variable at any of those
// construction points fails. This upgrades the prior Soft regex/string-anchor
// enforcement (PR #538) to a Hard "string-typed concept funnel" — the same
// mechanism kernel/governance.RuleCode uses (see
// governance_rules_invariants_test.go GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01).
//
// Funnel double-lock (ai-robust.md §"Funnel 双向锁评级"):
//   - Downstream Hard (集合外不能进): construction_funnel resolves every probe-name
//     key/arg to a declared *types.Const via info.Uses, rejecting BasicLit /
//     BinaryExpr / CallExpr / *types.Var. Identical to ruleCodeArgResolvesToConst.
//   - Upstream Hard (集合内必须经过): declaration_site_lock collects every real
//     healthz.ReadyProbeName *types.Const in production and flags any declared
//     outside the sanctioned package set; combined with the named type making a
//     bare string non-assignable to a ReadyProbeName const, the only authoring
//     path is "declare a typed const in a sanctioned package".
//
// Both locks are archtest-bound Hard (CI-enforced), the same档 as
// GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01. Go has no package-level const
// visibility seal, so a compile-time upstream seal is not expressible for this
// string-typed concept funnel shape — archtest-bound is the maximum achievable
// and is not a Medium-upstream transitional form requiring a Hard-ization issue.
//
// Out of scope (NOT ReadyProbeName, intentionally bare string): framework probes
// through healthz.NewProbe / bootstrap.WithHealthChecker (config_watcher,
// outbox_failopen_rate_<cell>, …) and runtime/outbox.Relay budget keys
// (outbox-relay-poll/-reclaim/-cleanup, hyphenated and non-_ready). Cell-level
// repo probes (cells/*/healthz_gen.go ProbeRepoReady) are codegen-funneled
// separately via RegisterRepoReady and are not touched here.
//
// Tool: RunTypedProduction (Pass-Driver) for production; RunTyped over testdata
// fixtures for the blind-spot self-checks. info.Uses / info.Types resolution +
// the EachInSubtree/EachInChildren scanner façade.
//
// Declared blind spots + reverse self-checks (ai-robust.md §"工具选定后强制盲区自检"):
//  1. Non-Ident key shapes — BasicLit "x_ready", BinaryExpr "x"+"_ready",
//     CallExpr fmt.Sprintf. Covered by testdata/ops_contract_string_funnel_fixtures/
//     bare_literal_key_red (asserts the construction scan flags them).
//  2. Runtime variable key — string(localVar) where localVar is a
//     healthz.ReadyProbeName *types.Var, not a const. info.Uses resolves to
//     *types.Var → rejected. Covered by .../local_var_key_red.
//  3. Declaration outside the sanctioned package set (a ReadyProbeName const
//     declared in a non-adapter package). Covered by .../decl_bypass_red, which
//     applies the sanctioned-package guard and asserts the const's package is
//     flagged.
//  4. A future adapter that authors probe names with a bare _ready string
//     CONSTANT in a package NOT in the sanctioned set escapes construction_funnel
//     (which only scans sanctioned packages). Compensation: the
//     sanctioned_set_covers_all_checkers meta-check loads production, discovers
//     every Checkers() returning a map[string]func with a _ready-shaped string
//     constant key (BasicLit / const ident / BinaryExpr concat, folded via
//     EvaluateConstString), and asserts that package is in the sanctioned set —
//     so a new bare-constant probe author fails loudly until it is funneled. The
//     HealthToCheckers-arg branch is scanned package-wide (not only inside
//     Checkers), so a probe authored from a helper method does not escape.
//  5. Shadowing the builtin `string` identifier inside a sanctioned Checkers
//     (so string(x) is not the conversion). Non-fixturable; compensation:
//     shadowing a predeclared identifier is independently caught by go vet /
//     golangci and is absurd in production adapters.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const opsContractStringFunnel01 = "OPS-CONTRACT-STRING-FUNNEL-01"

// readyProbeNameTypeName is the kernel/healthz named-string type that funnels
// adapter readiness-probe names.
const readyProbeNameTypeName = "ReadyProbeName"

// readyProbeValueShape is the snake_case + _ready ops-contract value shape
// (migrated from the deleted health_aggregation Soft scanner). Applied once per
// declared const rather than per construction site, since each value is now
// single-sourced.
var readyProbeValueShape = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*_ready$`)

// readyProbeSanctionedPkgs is the module-relative set of packages allowed to
// DECLARE healthz.ReadyProbeName consts and author probe-name constructions.
// A const declared elsewhere is a declaration-site bypass; a Checkers() in a
// package outside this set authoring _ready constants is caught by the
// sanctioned_set_covers_all_checkers meta-check.
//
// Maintenance: a NEW adapter readiness probe requires three coordinated edits —
// (1) declare a `const ProbeReady healthz.ReadyProbeName = "<name>_ready"` in the
// owning package; (2) add that package here; (3) add its entry to
// goldenReadyProbeNames(). adapters/adapterutil is intentionally NOT listed: it
// is the funnel boundary (HealthToCheckers's body does the sole string(name)
// conversion), not a probe-authoring site.
var readyProbeSanctionedPkgs = map[string]struct{}{
	"adapters/postgres": {},
	"adapters/redis":    {},
	"adapters/s3":       {},
	"adapters/rabbitmq": {},
	"adapters/vault":    {},
	"adapters/oidc":     {},
	"runtime/websocket": {},
}

// goldenReadyProbeNames freezes the full inventory of declared ReadyProbeName
// consts as "<module-relative-pkg>.<ConstName>=<value>". Adding, removing, or
// renaming any probe — including an observability dashboard mass rename — forces
// a deliberate one-line diff here, mirroring goldenRuleIDs() for RuleCode. Pair
// any edit with the corresponding readyProbeSanctionedPkgs change (see its godoc
// for the three-step new-probe checklist).
func goldenReadyProbeNames() []string {
	return []string{
		"adapters/oidc.ProbeReady=oidc_ready",
		"adapters/postgres.ProbeIndexesValidReady=postgres_indexes_valid_ready",
		"adapters/postgres.ProbeReady=postgres_ready",
		"adapters/rabbitmq.ProbeReady=rabbitmq_ready",
		"adapters/redis.ProbeReady=redis_ready",
		"adapters/s3.ProbeReady=s3_ready",
		"adapters/vault.ProbeReady=vault_transit_ready",
		"runtime/websocket.ProbeReady=websocket_hub_ready",
	}
}

// TestOpsContractStringFunnel is the OPS-CONTRACT-STRING-FUNNEL-01 entry point.
// A single production load drives declaration-site, construction, and
// value-shape checks plus the golden inventory; the testdata fixtures exercise
// the blind-spot self-checks against the shared scan helpers.
func TestOpsContractStringFunnel(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	// healthzPkgPath is the package const declared in readyz_probe_naming_test.go.

	var goldenEntries []string

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		pkgRel := strings.TrimPrefix(p.Pkg.Path(), modPath+"/")
		declared := collectReadyProbeNameConsts(p, healthzPkgPath)

		var ds []Diagnostic
		_, sanctioned := readyProbeSanctionedPkgs[pkgRel]

		// declaration-site lock (upstream Hard) — shared with the
		// decl_bypass_red fixture so that fixture is a real regression barrier
		// on this guard rather than a tautology.
		ds = append(ds, readyProbeDeclarationSiteViolations(pkgRel, declared, p.Fset)...)

		// value-shape (once per declared const) + golden accumulation.
		for c := range declared {
			val, ok := readyProbeConstValue(c)
			if !ok || !readyProbeValueShape.MatchString(val) {
				ds = append(ds, Diagnostic{
					Rel:  pkgRel,
					Line: p.Fset.Position(c.Pos()).Line,
					Message: readyProbeNameTypeName + " const " + strconv.Quote(c.Name()) +
						" value " + strconv.Quote(val) + " must be snake_case ending in _ready",
				})
			}
			if ok {
				goldenEntries = append(goldenEntries, pkgRel+"."+c.Name()+"="+val)
			}
		}

		// construction funnel (downstream Hard) — runs for EVERY sanctioned
		// package unconditionally: a bare-literal map key or HealthToCheckers
		// arg fails even when no const is declared yet, so the funnel guards
		// the authoring sites rather than merely the presence of consts.
		if sanctioned {
			for _, f := range p.Files {
				ds = append(ds, scanReadyProbeConstructionViolations(f, p.Fset, p.TypesInfo, p.Rel(f), declared)...)
			}
		}
		return ds
	})

	Report(t, opsContractStringFunnel01, diags)

	t.Run("golden_inventory", func(t *testing.T) {
		sort.Strings(goldenEntries)
		assert.Equal(t, goldenReadyProbeNames(), goldenEntries,
			"declared ReadyProbeName const inventory drifted from golden — update goldenReadyProbeNames() deliberately")
	})

	t.Run("sanctioned_set_covers_all_checkers", func(t *testing.T) {
		testOpsContractSanctionedSetCoverage(t, modPath)
	})

	t.Run("blind_spot_bare_literal_key", testOpsContractBareLiteralKeyFixture)
	t.Run("blind_spot_local_var_key", testOpsContractLocalVarKeyFixture)
	t.Run("blind_spot_declaration_bypass", testOpsContractDeclBypassFixture)
}

// collectReadyProbeNameConsts returns the package-scope *types.Const objects in
// p whose type is the real kernel/healthz.ReadyProbeName named type.
func collectReadyProbeNameConsts(p *Pass, healthzPkgPath string) map[*types.Const]struct{} {
	out := map[*types.Const]struct{}{}
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		named, ok := c.Type().(*types.Named)
		if !ok || named.Obj().Name() != readyProbeNameTypeName {
			continue
		}
		if named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != healthzPkgPath {
			continue
		}
		out[c] = struct{}{}
	}
	return out
}

// readyProbeDeclarationSiteViolations is the upstream-Hard declaration-site
// lock: a ReadyProbeName const may only be declared in a sanctioned ready-probe
// package. Returns one Diagnostic per declared const when pkgRel is NOT
// sanctioned. Shared between the production scan and the decl_bypass_red fixture
// so the fixture genuinely regresses this guard (delete the guard → fixture
// fails), instead of re-implementing the condition inline.
func readyProbeDeclarationSiteViolations(pkgRel string, declared map[*types.Const]struct{}, fset *token.FileSet) []Diagnostic {
	if _, sanctioned := readyProbeSanctionedPkgs[pkgRel]; sanctioned {
		return nil
	}
	var ds []Diagnostic
	for c := range declared {
		ds = append(ds, Diagnostic{
			Rel:  pkgRel,
			Line: fset.Position(c.Pos()).Line,
			Message: readyProbeNameTypeName + " const " + strconv.Quote(c.Name()) +
				" declared outside the sanctioned ready-probe package set; declare it in the" +
				" owning adapter package and add that package to readyProbeSanctionedPkgs",
		})
	}
	return ds
}

// readyProbeConstValue extracts the string value of a ReadyProbeName const.
func readyProbeConstValue(c *types.Const) (string, bool) {
	v := c.Val()
	if v.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(v), true
}

// scanReadyProbeConstructionViolations reports every Checkers() probe-name
// construction in file that does not resolve to a const in declared. Shared
// with the bare_literal_key_red / local_var_key_red fixtures so they exercise
// the production path. info must come from the same load as file.
func scanReadyProbeConstructionViolations(
	file *ast.File,
	fset *token.FileSet,
	info *types.Info,
	rel string,
	declared map[*types.Const]struct{},
) []Diagnostic {
	var ds []Diagnostic

	// (A) direct map[string]func(context.Context) error literal keys inside an
	// exported-receiver Checkers() method.
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "Checkers" || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Body == nil {
			return
		}
		recv := ReceiverTypeName(fn.Recv.List[0].Type)
		if recv == "" || !ast.IsExported(recv) {
			return
		}
		EachInSubtree[ast.CompositeLit](fn.Body, func(cl *ast.CompositeLit) {
			if !isReadyProbeCheckerMap(cl, info) {
				return
			}
			EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
				if probeNameExprResolves(kv.Key, info, declared) {
					return
				}
				ds = append(ds, Diagnostic{
					Rel:  rel,
					Line: fset.Position(kv.Key.Pos()).Line,
					Message: rel + "." + recv + " Checkers map key must reference a " + readyProbeNameTypeName +
						" const, e.g. string(ProbeReady) — got AST shape " + astShapeName(kv.Key),
				})
			})
		})
	})

	// (B) adapterutil.HealthToCheckers(<const>, …) first arg — scanned
	// package-wide (not only inside Checkers) so a probe name authored from a
	// helper method cannot escape the funnel.
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isHealthToCheckersCall(call, info) || len(call.Args) == 0 {
			return
		}
		if probeNameExprResolves(call.Args[0], info, declared) {
			return
		}
		ds = append(ds, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Args[0].Pos()).Line,
			Message: rel + " HealthToCheckers name arg must be a " + readyProbeNameTypeName +
				" const — got AST shape " + astShapeName(call.Args[0]),
		})
	})
	return ds
}

// probeNameExprResolves reports whether expr is <Ident> or string(<Ident>) and
// the inner Ident resolves via info.Uses to a *types.Const in declared.
// Fail-closed: nil info or any non-const resolution returns false.
func probeNameExprResolves(expr ast.Expr, info *types.Info, declared map[*types.Const]struct{}) bool {
	ident, ok := unwrapStringConversion(expr).(*ast.Ident)
	if !ok || info == nil {
		return false
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	_, found := declared[c]
	return found
}

// unwrapStringConversion returns the inner expression of a string(x) conversion;
// otherwise it returns expr unchanged. It only unwraps a CallExpr whose Fun is
// the bare identifier "string" with exactly one argument.
func unwrapStringConversion(expr ast.Expr) ast.Expr {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return expr
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "string" {
		return expr
	}
	return call.Args[0]
}

// isReadyProbeCheckerMap reports whether cl is a map[string]func(...) composite
// literal — the shape a Checkers() method returns.
func isReadyProbeCheckerMap(cl *ast.CompositeLit, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[cl]
	if !ok || tv.Type == nil {
		return false
	}
	m, ok := tv.Type.Underlying().(*types.Map)
	if !ok {
		return false
	}
	if !types.Identical(m.Key(), types.Typ[types.String]) {
		return false
	}
	_, isSig := m.Elem().Underlying().(*types.Signature)
	return isSig
}

// isHealthToCheckersCall reports whether call invokes
// adapters/adapterutil.HealthToCheckers.
func isHealthToCheckersCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && name == "HealthToCheckers" && strings.HasSuffix(pkgPath, "adapters/adapterutil")
}

// testOpsContractSanctionedSetCoverage is the upstream-Hard compensation
// (blind spot #4): any production Checkers() returning a map[string]func with a
// _ready-shaped string-CONSTANT key (BasicLit, const ident, or BinaryExpr
// concat — folded via EvaluateConstString) must live in a sanctioned package,
// so a new bare-literal probe author fails loud until funneled. A runtime
// (non-constant) key folds to ("", false) and is left to the construction
// funnel inside sanctioned packages.
func testOpsContractSanctionedSetCoverage(t *testing.T, modPath string) {
	var violations []string
	RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		pkgRel := strings.TrimPrefix(p.Pkg.Path(), modPath+"/")
		if _, ok := readyProbeSanctionedPkgs[pkgRel]; ok {
			return nil
		}
		for _, f := range p.Files {
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != "Checkers" || fn.Body == nil {
					return
				}
				EachInSubtree[ast.CompositeLit](fn.Body, func(cl *ast.CompositeLit) {
					if !isReadyProbeCheckerMap(cl, p.TypesInfo) {
						return
					}
					EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
						// Fold the key as a string constant: covers BasicLit,
						// const ident, and BinaryExpr concat. A non-constant
						// (runtime) key folds to ok=false and is ignored here.
						val, ok := EvaluateConstString(p.TypesInfo, kv.Key)
						if ok && readyProbeValueShape.MatchString(val) {
							violations = append(violations,
								pkgRel+" authors _ready probe "+strconv.Quote(val)+
									" with a bare string constant but is not in readyProbeSanctionedPkgs")
						}
					})
				})
			})
		}
		return nil
	})
	sort.Strings(violations)
	assert.Empty(t, violations,
		"a new ready-probe author must be added to readyProbeSanctionedPkgs and funneled through a ReadyProbeName const")
}

// testOpsContractBareLiteralKeyFixture proves construction_funnel flags
// BasicLit and BinaryExpr map keys (blind spot #1).
func testOpsContractBareLiteralKeyFixture(t *testing.T) {
	const pattern = "./tools/archtest/testdata/ops_contract_string_funnel_fixtures/bare_literal_key_red"
	violations := runFixtureConstructionScan(t, pattern)
	assert.GreaterOrEqual(t, len(violations), 2,
		"bare literal + concat map keys must both be flagged, got: %v", violations)
}

// testOpsContractLocalVarKeyFixture proves construction_funnel flags a
// string(localVar) key whose ident resolves to a *types.Var (blind spot #2).
func testOpsContractLocalVarKeyFixture(t *testing.T) {
	const pattern = "./tools/archtest/testdata/ops_contract_string_funnel_fixtures/local_var_key_red"
	violations := runFixtureConstructionScan(t, pattern)
	assert.GreaterOrEqual(t, len(violations), 1,
		"string(localVar) key must be flagged (Var, not Const), got: %v", violations)
}

// runFixtureConstructionScan loads a single-package fixture, collects its local
// ReadyProbeName-typed consts as the declared set, and runs the shared
// construction scan over its files.
func runFixtureConstructionScan(t *testing.T, pattern string) []Diagnostic {
	var out []Diagnostic
	RunTyped(t, TypedOpts{Tests: false}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		declared := collectLocalReadyProbeNameConsts(p)
		for _, f := range p.Files {
			out = append(out, scanReadyProbeConstructionViolations(f, p.Fset, p.TypesInfo, p.Rel(f), declared)...)
		}
		return nil
	})
	return out
}

// collectLocalReadyProbeNameConsts collects consts of a type named
// "ReadyProbeName" without the kernel/healthz package filter, for fixtures that
// declare a local mirror type (mirrors the filename_bypass_red isolation).
func collectLocalReadyProbeNameConsts(p *Pass) map[*types.Const]struct{} {
	out := map[*types.Const]struct{}{}
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		named, ok := c.Type().(*types.Named)
		if !ok || named.Obj().Name() != readyProbeNameTypeName {
			continue
		}
		out[c] = struct{}{}
	}
	return out
}

// testOpsContractDeclBypassFixture proves the declaration-site guard flags a
// ReadyProbeName const declared in a package outside the sanctioned set
// (blind spot #3). It drives the SAME production helper
// (readyProbeDeclarationSiteViolations) the main scan uses, so deleting that
// guard makes this fixture fail — it is not a tautology. The fixture uses a
// local mirror type; the guard logic (package ∉ sanctioned) is type-agnostic.
func testOpsContractDeclBypassFixture(t *testing.T) {
	const pattern = "./tools/archtest/testdata/ops_contract_string_funnel_fixtures/decl_bypass_red"
	var diags []Diagnostic
	RunTyped(t, TypedOpts{Tests: false}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		declared := collectLocalReadyProbeNameConsts(p)
		require.NotEmpty(t, declared, "fixture must declare a ReadyProbeName const")
		// The fixture package path is, by construction, not in the sanctioned
		// set, so the production guard must emit a violation.
		diags = readyProbeDeclarationSiteViolations(p.Pkg.Path(), declared, p.Fset)
		return nil
	})
	assert.NotEmpty(t, diags,
		"readyProbeDeclarationSiteViolations must flag a ReadyProbeName const declared outside the sanctioned package set")
}
