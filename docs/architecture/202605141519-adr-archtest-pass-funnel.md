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
| 3 | Meta-archtest `PASS-FUNNEL-EACHFILE-01` / `LOADPACKAGES-01` / `PACKAGES-IMPORT-01` re-detects bypass at test time via `*types.Info` resolution; symbol-level ban for `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.SharedResolver` plus packages-import path | **Hard** — type-aware | `typeseval.ResolvePackageRef` resolves call targets through go/types regardless of import alias / dot-import / vendor rewrites. Bypass requires editing `archtestmeta.LegacyAllowlist` (Go file, visible in diff). |

Three independent failure modes: type system, lint, archtest. Bypassing all three requires editing three independent locations in a single PR — reviewer-detectable by construction.

**`RunTypedDir` AI-rebust grade: Hard — three defenses hold unchanged.**

| # | Defense | Status after RunTypedDir |
|---|---------|--------------------------|
| 1 | `Pass.Pkg` is `*types.Package`, no `.Syntax` | **Unchanged** — `RunTypedDir` produces the same `*Pass` shape via `runTypedWithRoot`; authors still cannot reach `.Syntax`. |
| 2 | depguard bans direct `packages` import in `*_test.go` | **Unchanged** — `RunTypedDir` is a façade in `tools/archtest/pass.go`; business `*_test.go` files call the façade, not `packages.Load` directly. |
| 3 | `PASS-FUNNEL-LOADPACKAGES-01` funnel | **Widened (not weakened)** — `RunTypedDir` is the now-unique legitimate entry for fixture-module scanning. The banned set (`typeseval.LoadPackages` / `typeseval.SharedResolver`) is unchanged; `RunTypedDir` adds no new bypass surface because the fixture-module form previously had no approved entry point at all. Introducing `RunTypedDir` closes the gap rather than opening one. |

**Design decision D1 (Stage 1.6) — dual entry RunTyped / RunTypedDir, not a single merged function.**

Merging into `RunTyped(t, dir, …)` would force all call sites shipped in PR #492 / #493 / #496 and framework self-tests (`pass_test.go`) to update their signatures — violating the "0 second-pass rework" hard invariant. The two entries are semantically orthogonal: `RunTyped` = main tree via `findModuleRoot`; `RunTypedDir` = caller-specified absolute dir. Both delegate to the single `runTypedWithRoot` constructor — no logic fork. The E-class fixture-module files (`exported_error_new_fixtures_test.go` / `goose_session_locker_fixtures_test.go` / `prod_clock_injection_fixtures_test.go` / `prod_duration_fixtures_test.go` / `test_time_literal_fixtures_test.go`) used `RunTypedDir` with zero framework rework, as predicted — all migrated in PR #522.

**Why depguard bans only the `packages` import path, not `internal/scanner` / `internal/typeseval`**: those internal packages also export legitimate non-INV-1 helpers (`EachInSubtree`, `EachInChildren`, `ResolvePackageRef`, `ResolveMethodCall`, `EvaluateConstString`) that archtest authors should use directly. Path-level banning would force every legitimate user of those helpers to migrate to a façade wrapper or be exempted — bloat without security gain. Symbol-level banning of `EachFile` / `LoadPackages` / `SharedResolver` is the precise enforcement, and it requires `*types.Info` resolution which only the archtest layer (defense #3) provides. Lint stays narrow and Hard for the one path (`packages`) that is the load-bearing INV-1 reconstruction primitive.

**Cross-validation of allowlist drift**: `TestPassFunnelGuardListSync` archtest parses `.golangci.yml` at test time and asserts (a) every depguard negative-glob entry is a real archtest file present in `archtestmeta.LegacyAllowlist` or is a member of `passFunnelPermanentExempt` (`pass_funnel_test.go` / `pass_test.go` / `archtest_test.go`), (b) every file directly importing `golang.org/x/tools/go/packages` is in the depguard negative-glob list. Manual synchronization drift is fail-loud at archtest-run time, not at PR-review attention.

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

**E-class fixture-module files using `RunTypedDir`:** `exported_error_new_fixtures_test.go`, `goose_session_locker_fixtures_test.go`, `prod_clock_injection_fixtures_test.go`, `prod_duration_fixtures_test.go`, and `test_time_literal_fixtures_test.go` — all migrated as part of PR #522 (2026-05-16) consolidated batch. Zero framework rework was required as predicted.

## Termination criteria

The migration is complete when:

1. `archtestmeta.LegacyAllowlist` map is empty (asserted statically in stage-4 PR). **This was achieved in PR #522 (2026-05-16) via consolidated batch migration of all remaining 37 files.**
2. `.golangci.yml` `archtest-no-direct-packages-load.files` contains only the positive glob `**/tools/archtest/*_test.go` and the three permanent `passFunnelPermanentExempt` self-exemptions (`!**/tools/archtest/pass_funnel_test.go`, `!**/tools/archtest/pass_test.go`, `!**/tools/archtest/archtest_test.go`). The depguard `deny` list retains a single entry: `golang.org/x/tools/go/packages`.
3. The true end-state constraints are:
   - (a) `archtestmeta.LegacyAllowlist` is empty (criterion #1 above).
   - (b) No production archtest `*_test.go` imports `golang.org/x/tools/go/packages` directly — enforced by depguard, except the three `passFunnelPermanentExempt` files.
   - (c) No production archtest `*_test.go` directly calls `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.SharedResolver` / `typeseval.LoadProductionPackages` / `typeseval.EachFileInPackage` — enforced by PASS-FUNNEL meta-archtest via `*types.Info` resolution. **Note**: direct `import` of `internal/scanner` or `internal/typeseval` for their walk/resolve helpers (`EachInSubtree`, `EachInChildren`, `ResolvePackageRef`, `EvaluateConstString`, etc.) remains **allowed** — path-level banning of these packages is NOT done (see §Why-depguard). The funnel bans specific high-risk symbols, not the package paths.

After stage 4, `archtestmeta` is deleted entirely. `tools/archtest/internal/scanner` and `tools/archtest/internal/typeseval` retain their exported APIs — they are intentionally reachable from archtest test files for their non-INV-1 helpers (walk + go/types resolution). Symbol-level bans on `EachFile` / `LoadPackages` / `SharedResolver` are enforced by the PASS-FUNNEL meta-archtest (defense #3), not by lint.

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
