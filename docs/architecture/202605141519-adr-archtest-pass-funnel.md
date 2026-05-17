# ADR: archtest Pass-Funnel — single driver-constructed entry

## Status

Accepted — 2026-05-14. Refactor 574 is the stage-1 PR-1 implementation; stages 2 / 3 / 4 are tracked in `docs/plans/202605141519-040-archtest-pass-funnel-plan.md`.

## Context

Before this ADR, GoCell's archtest framework exposed two parallel rule-authoring entry points:

- **`scanner.EachFile(t, scope, mode, fn)`** — pure AST traversal, second-scale, organised by directory tree, 36 call sites / 25 archtest files.
- **`typeseval.LoadPackages(modRoot, tests, tags, patterns…)` + raw `for _, file := range pkg.Syntax`** — full go/types load, minute-scale, organised by import-path patterns, 33 archtest files / 48 raw for-range sites distributed across 30 files.

The two entry points compose at the Go-closure level, and the composition produces a recurring bug class we labelled **INV-1**: an archtest author iterates over files via `scanner.EachFile` but captures a `*types.Info` from a separate `typeseval.LoadPackages` call in the surrounding closure. The AST node pointers from the scanner load and the `info.Types` map keyed on a different load do not share identity → every lookup silently misses → the rule **never fires** and the missing enforcement only surfaces when downstream code starts violating the rule.

A PR earlier in the 543/552 series introduced `typeseval.EachFileInPackage` to suppress one specific shape of INV-1 (the "raw for-range inside typeseval users" form). That was a Medium-grade local fix. INV-1 remains expressible in any new code that mixes the two entry points outside that narrow site. The root cause is the *existence* of two parallel construction paths for the (Files, TypesInfo, Fset) tuple — not any single buggy site.

## Decision

Adopt the **Pass-Driver** paradigm pioneered by `go/analysis` and used uniformly by staticcheck, golangci-lint, wire, and ArchUnit:

```go
type Pass struct {
    Fset      *token.FileSet
    Files     []*ast.File
    Pkg       *types.Package    // nil in AST-only mode
    TypesInfo *types.Info       // nil in AST-only mode
    Rel       func(*ast.File) string
}

type Rule func(*Pass) []Diagnostic

func Run(t *testing.T, scope Scope, rule Rule) []Diagnostic
func RunTyped(t *testing.T, opts TypedOpts, patterns []string, rule Rule) []Diagnostic
func RunTypedDir(t testing.TB, dir string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic
```

Rule authors write `Rule` closures and let the driver (`Run` / `RunTyped` / `RunTypedDir`) construct `*Pass` from a single load. The framework owns `packages.Load` / `parser.ParseFile` timing; rule authors receive only `*Pass`.

**`RunTypedDir` API surface:**

- **Purpose**: load a standalone fixture module living under `testdata/` that carries its own `go.mod` + `replace` directives (intentional-violation isolation). This form is not addressable by `RunTyped` because `findModuleRoot` resolves the repo root, not the fixture subdirectory.
- **Internals**: delegates to `runTypedWithRoot(t, dir, opts, patterns, rule)` — the same single construction path shared with `RunTyped` (which delegates to `runTypedWithRoot(t, findModuleRoot(t), opts, patterns, rule)`). No fork in the driver logic.
- **`dir` constraint**: must be an absolute path; `filepath.IsAbs(dir)` is checked at entry with `t.Fatal` on violation.
- **Parameter type**: `testing.TB` (not `*testing.T`) to support fatal-path spy tests and `TestMain` callsites; consistent with `analysistest.Run`'s `Testing` interface.
- **Precedent**: `golang.org/x/tools/go/analysis/analysistest.Run(t, dir, analysers…)` feeds `dir` as `packages.Config.Dir`, allowing the loader to resolve imports relative to an arbitrary directory. `RunTypedDir` adopts the same position-parameter shape.

ref: `golang.org/x/tools go/analysis/analysistest/analysistest.go` (`dir` position param → `packages.Config.Dir`)

## Hard-line three-defense (AI-rebust ≥ Medium gating)

| # | Defense | Grade | Cost of violation |
|---|---------|-------|-------------------|
| 1 | `Pass.Pkg` is `*types.Package` (go/types stdlib), NOT `*packages.Package` (golang.org/x/tools/go/packages) | **Hard** — type system | Author cannot reach `.Syntax` from `*Pass`; INV-1 form is not expressible at the call site. Reconstructing INV-1 requires explicitly importing `golang.org/x/tools/go/packages` and calling `packages.Load`. |
| 2 | depguard rule `archtest-no-direct-packages-load` denies `golang.org/x/tools/go/packages` in `tools/archtest/*_test.go` (path-level import ban) | **Hard** — lint-blocking | Author must edit `.golangci.yml` to add their file to the negative-glob exemption (visible in diff, reviewer must approve). |
| 3 | Meta-archtest `PASS-FUNNEL-EACHFILE-01` / `LOADPACKAGES-01` / `PACKAGES-IMPORT-01` re-detects bypass at test time via `*types.Info` resolution; symbol-level ban for `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.SharedResolver` plus packages-import path | **Hard** — type-aware | `typeseval.ResolvePackageRef` resolves call targets through go/types regardless of import alias / dot-import / vendor rewrites. Bypass requires editing `passFunnelPermanentExempt` (3-entry Medium funnel, mechanical sync with .golangci.yml). |
| 4 | `RunTypedFixture` + `FixtureOpts` typed function choice with input-struct field exclusion (downstream) **AND** `PASS-FUNNEL-FIXTURE-TAG-01` `(callee, arg)`-pair type-aware ban in business `*_test.go` (upstream) | **Hard** — funnel double-lock (outward compile-time + upstream archtest-bound (callee, arg) form-uniqueness) | Downstream: `FixtureOpts` has no `Tags` field — `RunTypedFixture(t, FixtureOpts{Tags: ...}, ...)` is a compile error. Upstream: `diagsFixtureTagBypass` rejects any CallExpr whose callee resolves via `*types.Info` to a member of `fixtureTagLoaderSet` (archtest.{RunTyped, RunTypedProduction, RunTypedDir} + typeseval.{SharedResolver, LoadPackages, LoadProductionPackages}) AND any arg subtree contains an Expr whose `EvaluateConstString` result equals `"archtest_fixture"` — uniformly catching BasicLit literal / same-pkg const Ident / cross-pkg SelectorExpr (including `archtest.FixtureBuildTag`) / BinaryExpr const-concat. Isomorphic to charter §Hard 范本 第 2 条 `panic(panicregister.Approved(reason, value))` form (callee + arg pair, *types.Info-resolved, archtest-bound form-uniqueness — Go ceiling Hard). Go-code identity paths (callee NOT in loader set, e.g., `containsTag(group, archtest.FixtureBuildTag)`) remain legitimate. Same `passFunnelPermanentExempt` exempt set as defense #3 (3-entry framework files; `fixture.go` itself is excluded by the `*_test.go` suffix filter in `loadPassFunnelTargets`). |

Four independent failure modes: type system, lint, archtest (symbol-level), archtest ((callee, arg) form-uniqueness). Bypassing all four requires editing four independent locations in a single PR — reviewer-detectable by construction.

**`RunTypedDir` AI-rebust grade: Hard — three defenses hold unchanged.**

| # | Defense | Status after RunTypedDir |
|---|---------|--------------------------|
| 1 | `Pass.Pkg` is `*types.Package`, no `.Syntax` | **Unchanged** — `RunTypedDir` produces the same `*Pass` shape via `runTypedWithRoot`; authors still cannot reach `.Syntax`. |
| 2 | depguard bans direct `packages` import in `*_test.go` | **Unchanged** — `RunTypedDir` is a façade in `tools/archtest/pass.go`; business `*_test.go` files call the façade, not `packages.Load` directly. |
| 3 | `PASS-FUNNEL-LOADPACKAGES-01` funnel | **Widened (not weakened)** — `RunTypedDir` is the now-unique legitimate entry for fixture-module scanning. The banned set (`typeseval.LoadPackages` / `typeseval.SharedResolver`) is unchanged; `RunTypedDir` adds no new bypass surface because the fixture-module form previously had no approved entry point at all. Introducing `RunTypedDir` closes the gap rather than opening one. |

**Design decision D1 (Stage 1.6) — dual entry RunTyped / RunTypedDir, not a single merged function.**

Merging into `RunTyped(t, dir, …)` would force all call sites shipped in PR #492 / #493 / #496 and framework self-tests (`pass_test.go`) to update their signatures — violating the "0 second-pass rework" hard invariant. The two entries are semantically orthogonal: `RunTyped` = main tree via `findModuleRoot`; `RunTypedDir` = caller-specified absolute dir. Both delegate to the single `runTypedWithRoot` constructor — no logic fork. The E-class fixture-module files (`exported_error_new_fixtures_test.go` / `goose_session_locker_fixtures_test.go` / `prod_duration_fixtures_test.go` / `test_time_literal_fixtures_test.go`) used `RunTypedDir` with zero framework rework, as predicted — all migrated in PR #522. (`prod_clock_injection_fixtures_test.go` was migrated earlier by PR-6 / #500 (Stage 1.6), folded in there because it shares `scanProdClockInjectionAST` with `clock_invariants_test.go`; plan §1.6 is the single source of truth for its provenance.)

**Why depguard bans only the `packages` import path, not `internal/scanner` / `internal/typeseval`**: those internal packages also export legitimate non-INV-1 helpers (`EachInSubtree`, `EachInChildren`, `ResolvePackageRef`, `ResolveMethodCall`, `EvaluateConstString`) that archtest authors should use directly. Path-level banning would force every legitimate user of those helpers to migrate to a façade wrapper or be exempted — bloat without security gain. Symbol-level banning of `EachFile` / `LoadPackages` / `SharedResolver` is the precise enforcement, and it requires `*types.Info` resolution which only the archtest layer (defense #3) provides. Lint stays narrow and Hard for the one path (`packages`) that is the load-bearing INV-1 reconstruction primitive.

**Cross-validation of allowlist drift**: `TestPassFunnelGuardListSync` archtest parses `.golangci.yml` at test time and asserts two exact-equality invariants via maps.Equal + cmp.Diff: (1) yamlExempt == passFunnelPermanentExempt — every depguard negative-glob entry is one of the 3 permanent framework-self-exemption files, and vice versa; (2) packagesImport == passFunnelPermanentExempt — every archtest *_test.go that directly imports golang.org/x/tools/go/packages is one of the 3 permanent files, and vice versa. Manual synchronization drift between the yaml glob list, the Go map, and the file system is fail-loud at archtest-run time. (Stage 4 simplified from the earlier three-way subset checks against archtestmeta.LegacyAllowlist; the allowlist was cleared in PR #522 and the archtestmeta package was deleted in Stage 4.)

Three archtest framework files are **permanently** exempt — they form `passFunnelPermanentExempt` and survive stage-4 cleanup:

| File | Structural reason |
|------|-------------------|
| `pass_funnel_test.go` | Implements the PASS-FUNNEL meta-archtest itself; must reference the forbidden symbols. The type system cannot tell rule implementation from rule violator. |
| `pass_test.go` | Unit-tests `archtest.Run` / `RunTyped` / `buildTypedPass` / `newPackageRel` / `isPackageWithTestFiles`; the latter three accept or construct `*packages.Package` fixtures by signature. |
| `archtest_test.go` | Driver self-tests (LAYER-05..10 + PGQUERY-01) using `depgraph.FromPackages([]*packages.Package)` — this is the depgraph constructor, not a consumer archtest. Legitimate `*packages.Package` use is structurally identical to `pass_test.go`'s `buildTypedPass` input side. The `checkCellPublicAPIAdapterTypes` rule (LAYER-10) specifically requires `[]*packages.Package` to feed `tools/depgraph.FromPackages`; the Pass funnel deliberately hides `.Syntax` so the depgraph-based check is structurally inexpressible via the funnel — permanent exemption, tracked as backlog `ARCHTEST-LAYER10-PASS-MIGRATION-01`. |

These files are exempt from defense #2 (depguard yaml allowlist) and are skipped by defense #3's scanner (path matching against `passFunnelPermanentExempt`).

### AI-rebust honest caveats

Defense #1 is the only compile-time Hard. Defenses #2 and #3 are review-detectable Hards — the rule cannot police modifications to its own configuration / allowlist. The combined three-layer design accepts that elementary meta-governance boundary: the AI-rebust charter §"meta-governance" notes that "fault-redundant defenses in production code are over-engineering, in meta-governance are correct" — every additional meta layer has diminishing return because reviewers must always backstop the topmost layer.

### PR #522 amendment — threat matrix re-evaluation (2026-05-16)

PR #522 cleared `LegacyAllowlist` to zero via consolidated batch migration (37 remaining files). This **tightens** the funnel — every business archtest is now type-aware enforced with no allowlist escape hatch. The three permanent exemptions are funnel implementation/input files, not consumer archtests; they do not expand the upstream escape surface.

| Defense | Before PR #522 | After PR #522 | Status change |
|---------|----------------|---------------|---------------|
| #1 `Pass.Pkg *types.Package` (compile-time) | Active, no allowlist dependency | Unchanged | ✅ Unchanged Hard |
| #2 depguard `packages` import ban | Active; ~37 yaml negative-glob exemptions (LegacyAllowlist files) | Active; only 3 yaml negative-glob exemptions (permanent framework files) | ✅ Tightened — escape surface reduced from 37 to 3 |
| #3 PASS-FUNNEL meta-archtest (type-aware) | Active; 37 LegacyAllowlist runtime exemptions | Active; 0 LegacyAllowlist exemptions. 3 `passFunnelPermanentExempt` files skipped by path | ✅ Tightened — no business archtest can bypass via allowlist |
| LegacyAllowlist bypass surface | 37 files could use legacy entry points by declaration | 0 files | ✅ Eliminated |
| Permanent exemption escape surface | 1 file (`pass_funnel_test.go`) | 3 files (`pass_funnel_test.go` / `pass_test.go` / `archtest_test.go`) | ⚠️ Widened by 2 — mitigated: all 3 are framework internals, not consumer archtests; structural reasons documented in `passFunnelPermanentExempt` godoc; backlog `ARCHTEST-LAYER10-PASS-MIGRATION-01` tracks the `archtest_test.go` exemption follow-up |

**Conclusion**: LegacyAllowlist clearing makes the funnel strictly tighter for business archtests. The +2 permanent exemptions (`pass_test.go` + `archtest_test.go`) existed in practice since Stage 1 (they were depguard-exempted from the start) but were not formally named in the ADR. PR #522 makes them explicit in `passFunnelPermanentExempt`. No previously-clean defense row becomes ⚠️/❌ post-amendment.

### PR #536 review R1 amendment — façade bypass closure (2026-05-17, R1 + R1.1 combined)

PR #536 first-cut shipped `RunTypedFixture` + `FixtureOpts` as the downstream Hard ("outward Hard": `FixtureOpts` has no `Tags` field, compile-time block). The review caught that the upstream side was Soft: `RunTyped` still accepts arbitrary `Tags`, so business archtest could write `RunTyped(t, TypedOpts{Tags: []string{"archtest_fixture"}}, ...)` and bypass `RunTypedFixture` entirely — the new façade was self-discipline rather than enforced. The first-cut also retained one bare `"archtest_fixture"` literal in `http_contract_visibility_type_segregation_01_test.go:352` and one in `panic_invariants_test.go:367` (the latter unrelated to fixture loading: it identified the fixture tag group inside a module-wide `RunTyped` scan), both of which would have continued to pass review without an upstream rule.

R1 closes the upstream leg via two new pieces:

1. **`archtest.FixtureBuildTag` package-level const** (declared in `fixture.go`) — the typed-reference single source for any Go-code path that legitimately needs the tag name (e.g., the `panic_invariants_test.go` skip predicate). `RunTypedFixture`'s body now references the const (`Tags: []string{FixtureBuildTag}`), so the literal value appears exactly once in `archtest` source (the const RHS). Fixture sub-package `//go:build archtest_fixture` directives continue to hard-code the literal because Go build-directive syntax cannot reference a constant; the godoc on `FixtureBuildTag` documents this dual-source-by-construction relationship.

2. **`PASS-FUNNEL-FIXTURE-TAG-01` archtest** (`pass_funnel_test.go::diagsFixtureTagBypass`) — final form is type-aware on a (callee, arg) pair (see R1.1 below); the same `passFunnelPermanentExempt` 3-entry exempt set as defense #3 applies; `fixture.go` itself is excluded by the `*_test.go` suffix filter in `loadPassFunnelTargets`.

#### R1.1 detector form rewrite (same PR, post-R1 review)

R1 first attempt implemented `diagsFixtureTagBypass` as a `BasicLit` STRING walk — match every `*ast.BasicLit{Kind: STRING}` whose unquoted value equals `"archtest_fixture"` in business `*_test.go`. This caught the two known business call sites but the implementer-side review immediately flagged a critical form-uniqueness gap: the same R1 commit *also* introduced `archtest.FixtureBuildTag` as the typed-reference single source for Go-code paths, and any business archtest could write `RunTyped(t, TypedOpts{Tags: []string{FixtureBuildTag}}, ...)` — an `*ast.Ident` SelectorExpr that the BasicLit-only detector silently passes. The detector form was inheriting only half of charter §Hard 范本 第 2 条 (panic(any) range): the BasicLit STRING arg check, but **not** the callee `*types.Info` resolution. Without the callee dimension, the rule's "form uniqueness" reduces from "(callee, arg) pair" to "arg literal value", and the new const itself becomes a self-introduced bypass path. This is the same "Soft on Soft + patch" anti-pattern the charter §Review checklist warns against.

R1.1 corrects the detector form to its proper isomorph of panicregister.Approved:

- **Callee dimension**: CallExpr.Fun resolves via `typeseval.ResolvePackageRef` to a member of `fixtureTagLoaderSet`: `archtest.{RunTyped, RunTypedProduction, RunTypedDir}` + `typeseval.{SharedResolver, LoadPackages, LoadProductionPackages}`. `typeseval.EachFileInPackage` is intentionally excluded — its signature takes an already-loaded `*packages.Package`, not build tags, so it cannot be a fixture-tag bypass vector.
- **Arg dimension**: every arg subtree is `ast.Inspect`-walked; each Expr is passed to `typeseval.EvaluateConstString`; any Expr whose result equals `"archtest_fixture"` produces a diagnostic. The helper's existing resolution lattice (BasicLit / Ident → const / SelectorExpr → cross-pkg const / BinaryExpr → const-concat) catches all four shapes uniformly without per-shape code branches.

The (callee, arg) pair disambiguates legitimate identity uses from bypass: `containsTag(tagGroup, FixtureBuildTag)` in `panic_invariants_test.go` has the same arg-value resolution as the bypass form, but its callee (`containsTag`) is not in `fixtureTagLoaderSet`, so the detector does not fire there. The two semantically-distinct meanings — "identify the fixture tag group" vs "feed the fixture tag to a loader" — are now machine-distinguishable.

R1.1 also strengthens `TestPassFunnel_FixtureCoverage` from a single `≥1 total` assertion to per-form `≥1` independent trip-wires: `internal/passfunnelfixture/redfixture.go::fixtureTagBypassRedForms` exercises Forms A (BasicLit literal) / B (same-pkg const Ident) / C (BinaryExpr concat) / D (cross-pkg SelectorExpr `archtest.FixtureBuildTag`) via four `typeseval.SharedResolver` calls; removing any single form's line fails exactly that form's assertion. The R1 stale `_ = "archtest_fixture"` bare-literal RED line is deleted (V detector form no longer applies). The R1 detector's "BasicLit walk" / "String concatenation accept" Blind spot entries are deleted; R1.1 surfaces the new Blind spots: Tags arg as a non-const `*ast.Ident` (same-file var pattern) / cross-func var escape / reflect / fixtureTagLoaderSet enumeration maintenance — all same accept grade as the sister rules' identical Blind spots.

**Threat matrix re-evaluation (per ai-collab.md §ADR amendment 落地必查):**

| Defense | Before R1 (PR #536 first cut) | R1 first attempt (BasicLit walk) | R1.1 final ((callee, arg) form-uniqueness) | Status change |
|---------|-------------------------------|----------------------------------|--------------------------------------------|---------------|
| #1 `Pass.Pkg *types.Package` (compile-time INV-1 block) | Active | Unchanged | Unchanged | ✅ Unchanged Hard |
| #2 depguard `packages` import ban | Active; 3 yaml exemptions | Unchanged | Unchanged | ✅ Unchanged Hard |
| #3 PASS-FUNNEL meta-archtest (symbol-level: EachFile / LoadPackages / SharedResolver / LoadProductionPackages / EachFileInPackage / ResolvePackageRef helpers / ImportBan + packages-import path) | Active; 3 framework exemptions | Unchanged | Unchanged | ✅ Unchanged Hard |
| #4 RunTypedFixture downstream (FixtureOpts has no Tags) | Active outward Hard; **upstream Soft (no façade-bypass enforcement)** | Active outward Hard + upstream archtest-bound BasicLit-only Hard (**leaks Ident / SelectorExpr / BinaryExpr forms**) | Active outward Hard + upstream archtest-bound (callee, arg) form-uniqueness Hard (4 const-resolvable arg shapes covered) | ✅ Upgraded from Soft → Hard-with-gap → Hard double-lock closed |
| RunTypedFixture façade-bypass surface | 2 business call sites latently allowed | 0 business literal sites; **new const-reference bypass introduced by FixtureBuildTag** | 0 business call sites via any const-resolvable form | ✅ Eliminated completely |
| Permanent exemption escape surface | 3 files (`passFunnelPermanentExempt`) | Unchanged 3 files | Unchanged 3 files | ✅ Unchanged |
| Self-introduced bypass via own const | n/a | **FixtureBuildTag const reference passed to loader was silently legal** | Const reference in loader callee args is detected via EvaluateConstString → no self-introduced bypass | ✅ Closed |

**Conclusion**: R1.1 strictly tightens the funnel beyond R1 — defense #4 graduates from outward-Hard + upstream-Hard-with-BasicLit-only-gap to outward-Hard + upstream-Hard with full const-resolvable arg coverage. No previously-clean defense row becomes ⚠️/❌. `passFunnelPermanentExempt` size does not grow. ai-collab.md §Hard 范本 entry "typed function choice with input-struct field exclusion" has its 配套要求 paragraph naming R1.1 (not R1) as the reference implementation precedent, with explicit isomorphism to charter §Hard 范本 第 2 条 panic(panicregister.Approved) — both rules form-unique on a (callee, arg) pair via *types.Info resolution.

## Industry precedent

| Project | Pass shape | INV-1 defense |
|---------|-----------|---------------|
| **go/analysis** | `analysis.Pass` exposes `Files / TypesInfo / Fset / Pkg`; driver-private construction in `checker/checker.go`. **`Pass.Pkg` is `*types.Package`**, not `*packages.Package`. | Users author `Analyzer.Run(pass)`; cannot construct Pass freely. |
| **staticcheck** | Reuses `analysis.Pass`; helper functions `IsCallTo(pass, …)` take `*Pass`, not standalone `*types.Info`. | Same as go/analysis. |
| **golangci-lint** | Reuses `analysis.Pass` via the runner. | Same as go/analysis. |
| **wire** | `*gen{ pkg *packages.Package }` single field of truth; every method accesses AST + TypesInfo through that one `pkg`. | Single source by struct shape. |
| **ArchUnit (Java)** | `JavaClasses` physical representation (.class bytecode) — the dual-view problem is eliminated at the input layer. | N/A in Java. |
| **analysistest** | `analysistest.Run(t, dir, analysers…)` feeds `dir` as `packages.Config.Dir`, enabling arbitrary-directory fixture module loading. | Same Pass-Driver invariant — test author writes `Analyzer.Run(pass)`. |

The `*types.Package` (not `*packages.Package`) shape is the **load-bearing** detail: it is what makes `pass.Pkg.Syntax` a compile error rather than a runtime check.

## Migration path (four stages)

Strategic plan: `docs/plans/202605141519-040-archtest-pass-funnel-plan.md`. Summary:

- **Stage 1** (this PR, refactor/574): Land the Pass framework + 3 Hard defenses + LegacyAllowlist of 53 existing archtests. Zero business archtest changes; the new framework coexists with the legacy entry points behind allowlist exemption.
- **Stage 1.5** (PR #495): Framework completion — `Pass.Abs`, `IsFileInScope`, `IsGenerated`, `resolve.go` façade free funcs, `PASS-FUNNEL-RESOLVE-01` meta-archtest. Closes all known API gaps so Stages 2/3/dual become zero-framework-return mechanical migrations.
- **Stage 1.6** (PR-6, concurrent with Stage 3 PR-6): `RunTypedDir` façade — closes the fixture-module scanning gap discovered during `clock_invariants_test.go` migration. See §Stage 1.6 below.
- **Stages 2 / 3** (~9 PRs planned, delivered as ~7 PRs + 1 consolidated): Migrate the 53 legacy archtests theme-by-theme. Stage 2 was completed in PR-2..PR-5 (#492/#493/#496/#497/#498). Stage 3 PR-6 (#500) and PR-7 (#507) shipped; the remaining 37 files were consolidated into PR #522 (2026-05-16) — batch migration proved more tractable than three separate PRs due to shared-helper graph coupling. Each migration PR is the **first and only** edit to its target archtest (import + API + semantics in one commit), removes entries from `LegacyAllowlist` AND the matching negative-glob in `.golangci.yml`. PR #522 cleared `LegacyAllowlist` to zero.
- **Stage 4**: Delete `archtestmeta` package entirely; collapse the negative-glob exemption block in `.golangci.yml` to retain only the three `passFunnelPermanentExempt` entries (`!**/tools/archtest/pass_funnel_test.go`, `!**/tools/archtest/pass_test.go`, `!**/tools/archtest/archtest_test.go`); delete the LegacyAllowlist reference from `pass_funnel_test.go`. Defense #1 (`Pass.Pkg` shape) and defense #2 (depguard deny list) remain permanent; defense #3 retains the three permanent framework-file exemptions. **Note**: PR #522 (2026-05-16) already cleared `LegacyAllowlist` via consolidated batch migration; Stage 4 work remaining is the `archtestmeta` package deletion and yaml cleanup.

  **Shipped in PR #PENDING (2026-05-17).** Full change list:
  - `tools/archtest/internal/archtestmeta/` package deleted entirely (LegacyAllowlist was empty since PR #522)
  - `tools/archtest/fixture.go` added: `type FixtureOpts struct { Tests bool }` + `RunTypedFixture(t *testing.T, opts FixtureOpts, patterns []string, rule Rule) []Diagnostic` — Hard typed funnel for fixture loading; `FixtureOpts` has no `Tags` field, making "business supplies custom build tag" inexpressible at the type level. Parameter type `*testing.T` (not `testing.TB`): fixture loading has no spy fatal-path requirement, aligning with `RunTyped` / `RunTypedProduction`; orthogonal to `RunTypedDir` which uses `testing.TB` for standalone-fixture-module spy testing
  - `tools/archtest/pass_test.go`: 6 `RunTyped(…Tags: []string{archtestmeta.FixtureBuildTag}…)` calls replaced with `RunTypedFixture(…FixtureOpts{Tests: bool}…)`; `archtestmeta` import deleted; 3 new TDD tests added (`TestRunTypedFixture_LoadsRedfixture`, `TestRunTypedFixture_TestVariantLoad`, `TestRunTypedFixture_FixtureOptsLacksTagsField`)
  - `tools/archtest/pass_funnel_test.go`: `TestPassFunnelGuardListSync` rewritten as two single equality assertions (`maps.Equal` + `cmp.Diff`; LegacyAllowlist cross-validation removed); `loadPassFunnelTargets` LegacyAllowlist filter line deleted; `archtestmeta.FixtureBuildTag` → `"archtest_fixture"` literal; `TestArchtestmetaPackageDeleted` static reverse-lock added; `passFunnelPermanentExempt` godoc updated with Medium AI-rebust evaluation; package-level godoc updated to reflect Stage 4 terminal state
  - `.golangci.yml`: migration-period comment block removed from `archtest-no-direct-packages-load` section; 3 permanent self-exemptions and deny rule retained; ADR §Termination criteria cross-reference added
  - `ai-collab.md` §载体决策原则 §3 rewritten with `archtest.*` public façade routing; anti-misuse note added (existing files importing internal helpers remain valid per ADR §163); §Hard 范本 new entry "typed function choice with input-struct field exclusion" added
  - Minor comment updates: `resolve.go`, `adapter_error_classification_test.go`, `passfunnelfixture/redfixture.go`, `basesliceredfixture/base_slice_literal.go`, `basesliceredfixture/slice_meta_literal.go`

  **Review R1 closure (same PR, post-first-cut commits):** PR #536 review caught that defense #4 was outward Hard only — the upstream side (façade bypass via `RunTyped(t, TypedOpts{Tags: []string{"archtest_fixture"}}, ...)`) remained Soft. Two business call sites latently allowed by Soft upstream (`http_contract_visibility_type_segregation_01_test.go:352` + `panic_invariants_test.go:367`) were not flagged by the first-cut funnel. R1 fixes:
  - `tools/archtest/fixture.go`: add exported `FixtureBuildTag = "archtest_fixture"` const; `RunTypedFixture` body references the const (`Tags: []string{FixtureBuildTag}`); godoc rewrites the prior "no FixtureBuildTag const" justification (the //go:build side cannot reference a Go constant, but Go-code paths can — that gap is what the const fills); funnel double-lock made explicit in godoc (outward Hard + upstream Hard + inward Medium)
  - `tools/archtest/pass_funnel_test.go`: add `diagsFixtureTagBypass` + `TestPassFunnelFixtureTagBypass01` (PASS-FUNNEL-FIXTURE-TAG-01); augment `TestPassFunnel_FixtureCoverage` with a ≥1 coverage assertion on the new detector; fix stale `LegacyAllowlist` reference at line 258 → `passFunnelPermanentExempt` (R2)
  - `tools/archtest/internal/passfunnelfixture/redfixture.go`: add `_ = "archtest_fixture"` bare-literal RED for the new detector + godoc explanation
  - `tools/archtest/http_contract_visibility_type_segregation_01_test.go`: helper signature changed from `(tags []string, patterns)` to `(fixture bool, patterns)`; fixture=true branch uses `RunTypedFixture`; fixture=false branch uses `RunTyped`
  - `tools/archtest/panic_invariants_test.go`: `containsTag(tagGroup, "archtest_fixture")` → `containsTag(tagGroup, FixtureBuildTag)` (typed-reference path)
  - 6 fixture-package godoc blocks (`rawparamfixture`, `auditledgerfixture`, `inspectorredfixture`, `wrapfixture/violation`, `sessionprotocolfixture`, `refreshinvariantsfixture`): example "loaded via `typeseval.SharedResolver` with tags=[]string{...}" rewritten to `archtest.RunTypedFixture(...)`
  - 2 basesliceredfixture + 1 passfunnelfixture godoc blocks: literal-reference text rewritten to reference `archtest.FixtureBuildTag` const (with //go:build-cannot-reference-const justification preserved)
  - `ai-collab.md` §Hard 范本 entry "typed function choice with input-struct field exclusion": add **配套要求** paragraph mandating the upstream meta-archtest in the same PR
  - This ADR: defense #4 row added to §Hard-line three-defense; §PR #536 review R1 amendment subsection added (this section) with full threat matrix re-evaluation per ai-collab.md §ADR amendment 落地必查; §Termination criteria (c) extended below
  - CI: `go test ./tools/archtest/... -count=1` all green (151.6s); `hack/verify-archtest.sh` 16-shard process-isolated all green (TOTAL=458, +1 from baseline TOTAL=457 via ARCHTEST-VERIFY-COVERAGE-01 auto-discovery)

## Stage 1.6 — RunTypedDir fixture-module driver (2026-05-15, shipped with Stage 3 PR-6)

**Root cause addressed.** Stage 1.5 froze the `RunTyped` API without inventorying the `testdata/`-local standalone fixture module form: some archtest fixture packages carry their own `go.mod` + `replace` directives to isolate intentional-violation code from the main module graph. `RunTyped` uses `findModuleRoot` which resolves the repository root, making these subdirectory modules unreachable. `clock_invariants_test.go` (Stage 3 PR-6) was the first of the 32 E-class files to hit this form; the framework gap is closed in the same PR rather than deferred.

**API added:**

```go
// RunTypedDir loads the Go package(s) matching patterns rooted at dir
// rather than the repository module root. Use this for testdata/ subdirectories
// that carry their own go.mod (standalone fixture modules).
//
// dir must be an absolute path (filepath.IsAbs checked; t.Fatal on violation).
// Internally delegates to runTypedWithRoot(t, dir, opts, patterns, rule) —
// the same single construction path as RunTyped.
//
// ref: golang.org/x/tools go/analysis/analysistest/analysistest.go (dir → packages.Config.Dir)
func RunTypedDir(t testing.TB, dir string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic
```

**Single construction path invariant.** Both `RunTyped` and `RunTypedDir` delegate to the internal `runTypedWithRoot(t, root, opts, patterns, rule)` function. The two façade entries differ only in how `root` is obtained (`findModuleRoot(t)` vs the caller-supplied `dir`). No duplication of load logic; no new INV-1 surface.

**AI-rebust grade: Hard — see §Hard-line three-defense `RunTypedDir` subsection above.**

**E-class fixture-module files using `RunTypedDir`:** `exported_error_new_fixtures_test.go`, `goose_session_locker_fixtures_test.go`, `prod_duration_fixtures_test.go`, and `test_time_literal_fixtures_test.go` — all migrated as part of PR #522 (2026-05-16) consolidated batch. Zero framework rework was required as predicted. `prod_clock_injection_fixtures_test.go` is **not** in PR #522: it was migrated by PR-6 / #500 (Stage 1.6) due to its `scanProdClockInjectionAST` coupling with `clock_invariants_test.go` — see plan §1.6 (single source of truth).

## Termination criteria

The migration is complete when: (**All three achieved in PR #PENDING 2026-05-17.**)

1. ✅ `archtestmeta.LegacyAllowlist` map is empty AND package deleted. **LegacyAllowlist emptied in PR #522 (2026-05-16); package deleted in PR #PENDING (2026-05-17). `TestArchtestmetaPackageDeleted` static reverse-lock prevents regression.**
2. ✅ `.golangci.yml` `archtest-no-direct-packages-load.files` contains only the positive glob `**/tools/archtest/*_test.go` and the three permanent `passFunnelPermanentExempt` self-exemptions (`!**/tools/archtest/pass_funnel_test.go`, `!**/tools/archtest/pass_test.go`, `!**/tools/archtest/archtest_test.go`). The depguard `deny` list retains a single entry: `golang.org/x/tools/go/packages`. **Achieved in PR #PENDING (2026-05-17) — migration-period comment block removed.**
3. ✅ The true end-state constraints are:
   - (a) `archtestmeta.LegacyAllowlist` is empty and package deleted (criterion #1 above). `archtestmeta` package deleted entirely; `RunTypedFixture` typed helper supplies the build-tag literal via function-body inlining (single source for the `archtest_fixture` string; Go build directive syntax does not allow const reference).
   - (b) No production archtest `*_test.go` imports `golang.org/x/tools/go/packages` directly — enforced by depguard, except the three `passFunnelPermanentExempt` files.
   - (c) No production archtest `*_test.go` directly calls `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.SharedResolver` / `typeseval.LoadProductionPackages` / `typeseval.EachFileInPackage` — enforced by PASS-FUNNEL meta-archtest via `*types.Info` resolution. **Note**: direct `import` of `internal/scanner` or `internal/typeseval` for their walk/resolve helpers (`EachInSubtree`, `EachInChildren`, `ResolvePackageRef`, `EvaluateConstString`, etc.) remains **allowed** — path-level banning of these packages is NOT done (see §Why-depguard). The funnel bans specific high-risk symbols, not the package paths.
   - (d) No production archtest `*_test.go` CallExpr resolves to a `fixtureTagLoaderSet` member (archtest.{RunTyped, RunTypedProduction, RunTypedDir} + typeseval.{SharedResolver, LoadPackages, LoadProductionPackages}) with any arg subtree containing an Expr whose `EvaluateConstString` value equals `"archtest_fixture"` — enforced by PASS-FUNNEL-FIXTURE-TAG-01 (defense #4 upstream). Catches BasicLit literal / same-pkg const Ident / cross-pkg SelectorExpr (incl. `archtest.FixtureBuildTag`) / BinaryExpr const-concat uniformly. Fixture loading uses `archtest.RunTypedFixture` (the literal is supplied inside the funnel body, single source = `archtest.FixtureBuildTag` const); Go-code tag-identity paths (callee NOT in loader set) reference `archtest.FixtureBuildTag` const. Added in PR #536 review R1 (2026-05-17), corrected to (callee, arg) pair form in same-PR R1.1 rewrite to close BasicLit-only Soft gap.

`tools/archtest/internal/scanner` and `tools/archtest/internal/typeseval` retain their exported APIs — they are intentionally reachable from archtest test files for their non-INV-1 helpers (walk + go/types resolution). Symbol-level bans on `EachFile` / `LoadPackages` / `SharedResolver` are enforced by the PASS-FUNNEL meta-archtest (defense #3), not by lint.

### §passFunnelPermanentExempt — Medium AI-rebust evaluation

(Extension of the "Permanent exemption escape surface" row at L100 above; see also `passFunnelPermanentExempt` godoc in `pass_funnel_test.go`.)

The three permanent exemption files (`pass_funnel_test.go` / `pass_test.go` / `archtest_test.go`) form `passFunnelPermanentExempt`. The set is **AI-rebust Medium** (not Soft, not Hard):

| Dimension | Evaluation |
|-----------|------------|
| Set closure | ✅ 3 entries enumerated exhaustively in this ADR; adding a new entry requires modifying both the Go map literal AND the `.golangci.yml` negative-glob list |
| Mechanical sync | ✅ Double-declaration: Go map literal in `pass_funnel_test.go` + matching `!**/tools/archtest/<file>` negative globs in `.golangci.yml`. `TestPassFunnelGuardListSync` asserts exact equality between the two declarations (`maps.Equal` + `cmp.Diff`); single-sided drift causes CI failure |
| Hard upgrade feasibility | ❌ Not feasible without creating a new package boundary (`archtestself`) that adds architectural complexity without security gain: the 3 files structurally must import `golang.org/x/tools/go/packages` (framework internals, depgraph constructor input side); type system cannot distinguish rule implementation from rule violator |
| Is this a new Soft escape | ✅ No: the 3-file permanent exemption has existed since Stage 1 (all 3 were in the depguard negative-glob list from PR #492); Stage 4 formalizes the set name and adds exact-equality enforcement |

Conclusion: `passFunnelPermanentExempt` is **Medium** — mechanical sync via double-declaration + exact-equality assertion; structural necessity, not Soft escape.

## Stage 1.5 — Framework completion + single-path enforcement (2026-05-15)

**Root cause addressed.** PR #492 froze the `Pass`/`Run`/`RunTyped`/façade API without a complete inventory of what every existing archtest (24 A-class + 32 E-class + 6 dual-class) actually extracts from source. A surface-level patch is L1 thinking: PR #493's contract/codegen migration was already forced to hand-write `parser.ParseFile(ParseComments)` inside `codegen_invariants`/`listener_dx` to work around the gap — guaranteed second-pass rework. Stage 1.5 fixes the framework end-state **once** and closes the legacy path in the same PR so Stage 2/3/dual-class become zero-framework-return mechanical migrations.

**Verified facts (source-line, no "probably"):**

- AST path (`archtest.Run` → `collectASTFiles`) parsed without comments. **Fixed**: parse mode now `parser.SkipObjectResolution | parser.ParseComments`.
- Typed path (`RunTyped`/typeseval) **already carries comments** — `golang.org/x/tools/go/packages` default `ParseFile` is `parser.AllErrors | parser.ParseComments` and typeseval sets no custom `cfg.ParseFile`. **No typeseval change**; locked by `TestRunTyped_CommentsRegressionLock`.
- `FileContext` exposes no `Bytes`; raw bytes live only on `ContentContext` via the already-re-exported `EachContentFile`. **Not a gap.**
- Absolute path was internally computed (`fset.Position(f.Pos()).Filename`) but not exposed. **Fixed**: `Pass.Abs func(*ast.File) string`, single-sourced with `Rel`, zero new state.

**Façade end-state (the import invariant).** Business archtest end-state import set is exactly `tools/archtest` — zero `internal/scanner` / `internal/typeseval` / `x/tools/go/packages`. The 14 typeseval symbols are partitioned:

| Class | Symbols | End-state |
|---|---|---|
| Loader / load primitive | `LoadPackages` `SharedResolver` `LoadProductionPackages` `Resolver` `ProductionResolver` `EachFileInPackage` | **Never re-exported** — reachable only via `RunTyped` / `RunTypedDir` (this is the funnel Hard defense body) |
| info-taking pure helper | `ResolvePackageRef` `ResolveMethodCall` `EvaluateConstString` | `resolve.go` free funcs (extends Alt-D rationale: helpers take `*types.Info`, not privatized) |
| build-constraint | `ParseBuildConstraint` `BuildContextPredicate` `IsGeneratedRelPath` | `(*Pass).IsFileInScope` / `(*Pass).IsGenerated` methods for the default-predicate bool case; **plus** free funcs `archtest.ParseBuildConstraint` / `archtest.IsGeneratedRelPath` for call sites that need the raw `constraint.Expr` or a raw-string rel path (see erratum below) |
| tags preset | `FlatNonDefaultTags` `KnownNonDefaultTags` | `resolve.go` free funcs |

Scanner side: only `scanner.ImportBan` was unexported (re-exported as `type ImportBan = scanner.ImportBan`); it does **not** collapse into depguard — depguard cannot express its file-local `AllowRels` semantics (§ Termination criteria reasoning).

**Two-defense single-path closure (AI-rebust graded, ≥ Medium gating met):**

| # | Mechanism | Grade | Blind-spot evidence |
|---|---|---|---|
| 1a | `TestFacadeDoesNotLeakLoaders` — banned-name absence: static assertion that the façade exports none of the 6 banned loader names | **Hard** — symbol not present in the façade's exported set at compile time; a business `*_test.go` that writes `archtest.LoadPackages(...)` fails to compile. There is no "looks like the symbol but isn't" gray zone for the banned names specifically. | reverse self-check enumerates façade exports, asserts loader-name-set ∩ export-set = ∅ |
| 1b | `TestFacadeDoesNotLeakLoaders` — `*packages.Package` signature-type detection: AST string-match on `packages` ident in exported func/type signatures | **Medium** — AST string-match on the `packages` ident in `SelectorExpr.X`; bypassable via import alias (`import pkgs ".../go/packages"`). Backup: defense #3 `LOADPACKAGES-01` detects `packages.Load` / `SharedResolver` via `*types.Info` resolution, catching alias forms. | honest disclosure in `TestFacadeDoesNotLeakLoaders` godoc; LOADPACKAGES-01 type-aware detector is the backup |
| 2 | `PASS-FUNNEL-RESOLVE-01` — type-aware (`*types.Info` via `typeseval.ResolvePackageRef`, reusing the `diagsLoadPackages` symbol-set machinery) ban on business archtest direct-calling the 8 helper symbols + `scanner.ImportBan`; legacy exempt via `LegacyAllowlist`, stage-4 zeroed | **Medium** (type-aware call interception + allowlist cross-validate; symmetric to the existing loader rule — not godoc Soft) | `diagsResolveHelpers` godoc lists blind-spot forms (value indirection / cross-file indirection); `TestPassFunnel_FixtureCoverage` exact-count assertion (ImportBan == 3 for qualified+alias+dot-import) is the live regression lock; dot-import `ImportBan` is resolver-covered via `*types.TypeName` in `resolveBarePkgSymbol` AND fixtured at L125 |

The legacy `internal/typeseval` direct-import path is **closed in the same PR** (not deprecated-aliased): the allowlist is a terminating todo (stage-4 deletes it), consistent with Alt-C's rejection of deprecation aliases. This makes the "business imports only `archtest`" end-state Hard+Medium-enforced, not a godoc Soft convention.

**Erratum — dot-import TypeName fix (PR #492 patch):** The original Stage 1.5 description assumed `typeseval.ResolvePackageRef`'s dot-import resolution covered only `*types.Func` (functions). `scanner.ImportBan` is a struct type, resolved via `*types.TypeName`. The resolver's `resolveBarePkgSymbol` only handled the `*types.Func` branch, leaving bare-Ident `ImportBan{}` (dot-import form) undetected. Fixed by adding a `*types.TypeName` branch in `resolveBarePkgSymbol`; the `TestPassFunnel_FixtureCoverage` `== 3` exact-count assertion (qualified + alias + dot-import) would drop to 2 if this fix were reverted. Committed in `8f4123f5`.

**Erratum — RESOLVE-01 façade-exit completeness (PR #495):** The RESOLVE-01 banned-set ↔ façade-exit mapping was not exhaustively verified at Stage 1.5 time. Two symbols had no adequate façade exit:

- `ParseBuildConstraint`: `build_constraint_test.go` and `ci_integration_discovery_invariants_test.go` call it with a raw file path and use the returned `constraint.Expr` for 3-way evaluation under custom predicates. `Pass.IsFileInScope` returns only a single bool with the default predicate and cannot cover this use-case.
- `IsGeneratedRelPath`: `outbox_invariants_test.go:TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3` calls it with a raw string `rel` derived from `pkgFileRel`, not from a `*ast.File` via `pass.Rel`. `Pass.IsGenerated(f *ast.File)` cannot cover a raw-string call site outside a Pass-Driver rule.

Both gaps were closed in PR #495 by adding `func ParseBuildConstraint(filePath string) (constraint.Expr, error)` and `func IsGeneratedRelPath(rel string) bool` as thin-delegate free funcs in `resolve.go`. Both symbols remain in the RESOLVE-01 banned map (the ban targets `typeseval.`-qualified direct calls; business code uses the `archtest.` façade). The now-true invariant: **every RESOLVE-01 banned symbol has a façade exit semantically sufficient for all existing call sites**.

**Rejected noise (would violate elegance):** `TypedOpts.ParserMode` knob (always-ParseComments is additive and simpler); `Pass.FilePackage` closure (`RunTyped` is one-Pass-per-package; `pass.Pkg` already is the owner); re-exporting any loader (would defeat defense #1).

## Rejected alternatives

### Alt-A: Keep two entry points, add a "linting middleware" between them

Build a runtime check that fails when AST nodes from one load are paired with `*types.Info` from another. Rejected: the runtime check fails only on bugs that have already shipped (and only the bugs that trigger the mismatch detection — silent type-info-miss bugs do not trigger). Type-system Hard is strictly stronger.

### Alt-B: Merge `internal/scanner` and `internal/typeseval` into one package

Would produce a giant internal package with 25+ exported symbols (12 + 12 + 1). Rejected for ergonomics: scanner has the right cohesion as "AST framework", typeseval as "go/types helpers"; merging would not help INV-1 anyway because the bug class is about *user code* mixing the two — not about the internal split.

### Alt-C: Deprecated-alias migration (`scanner.EachFile` stays callable, emits warning)

Rejected per GoCell's "不向后兼容" principle: deprecated aliases create migration ambiguity ("which call site is the source of truth?") and lengthen the migration tail. The LegacyAllowlist explicitly carries a terminate point (stage 4 deletes it); deprecated aliases would not.

### Alt-D: Privatize `*types.Info` behind `Pass` methods

Would re-implement every typeseval helper (`ResolvePackageRef`, `ResolveMethodCall`, `EvaluateConstString` …) as `Pass` methods. Rejected: large API surface to maintain, and the type-aware INV-1 defense is already secured by the `Pass.Pkg *types.Package` shape (defense #1). `pass.TypesInfo` is a public field, consistent with `analysis.Pass.TypesInfo` upstream.

### Alt-E (Stage 1.6): Merge RunTyped and RunTypedDir into a single `RunTyped(t, dir, …)` with optional dir

Rejected: merging would force signature changes on all call sites shipped in PR #492 / #493 / #496 and the framework self-tests, violating the "0 second-pass rework" hard invariant. Dual-entry with shared `runTypedWithRoot` constructor achieves the same result without any existing call-site churn. See plan §D6.

## References

- `docs/plans/202605141519-040-archtest-pass-funnel-plan.md` — strategic plan with 4-stage migration and parallelism analysis.
- `.claude/rules/gocell/ai-collab.md` — AI-rebust charter (Hard / Medium / Soft grading, vehicle decision principles, archtest naming).
- `tools/archtest/scanner_framework_usage_test.go` — `SCANNER-FRAMEWORK-USAGE-01` (sibling Hard meta-archtest; structural template for `pass_funnel_test.go`).
- `go/analysis.Pass` — upstream Pass shape ([pkg.go.dev/golang.org/x/tools/go/analysis](https://pkg.go.dev/golang.org/x/tools/go/analysis)).
- `docs/architecture/202605120000-adr-archtest-process-isolation.md` — `hack/verify-archtest.sh` 16-shard CI infrastructure; pass_funnel_test.go is discovered automatically.

ref: golang.org/x/tools `analysis.Pass.Pkg = *types.Package`; uber-go/wire `gen.pkg` single field of truth; golang.org/x/tools `go/analysis/analysistest/analysistest.go` (`dir` → `packages.Config.Dir` for fixture-module loading).
