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
```

Rule authors write `Rule` closures and let the driver (`Run` / `RunTyped`) construct `*Pass` from a single load. The framework owns `packages.Load` / `parser.ParseFile` timing; rule authors receive only `*Pass`.

## Hard-line three-defense (AI-rebust ≥ Medium gating)

| # | Defense | Grade | Cost of violation |
|---|---------|-------|-------------------|
| 1 | `Pass.Pkg` is `*types.Package` (go/types stdlib), NOT `*packages.Package` (golang.org/x/tools/go/packages) | **Hard** — type system | Author cannot reach `.Syntax` from `*Pass`; INV-1 form is not expressible at the call site. Reconstructing INV-1 requires explicitly importing `golang.org/x/tools/go/packages` and calling `packages.Load`. |
| 2 | depguard rule `archtest-no-direct-packages-load` denies `golang.org/x/tools/go/packages` in `tools/archtest/*_test.go` (path-level import ban) | **Hard** — lint-blocking | Author must edit `.golangci.yml` to add their file to the negative-glob exemption (visible in diff, reviewer must approve). |
| 3 | Meta-archtest `PASS-FUNNEL-EACHFILE-01` / `LOADPACKAGES-01` / `PACKAGES-IMPORT-01` re-detects bypass at test time via `*types.Info` resolution; symbol-level ban for `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.SharedResolver` plus packages-import path | **Hard** — type-aware | `typeseval.ResolvePackageRef` resolves call targets through go/types regardless of import alias / dot-import / vendor rewrites. Bypass requires editing `archtestmeta.LegacyAllowlist` (Go file, visible in diff). |

Three independent failure modes: type system, lint, archtest. Bypassing all three requires editing three independent locations in a single PR — reviewer-detectable by construction.

**Why depguard bans only the `packages` import path, not `internal/scanner` / `internal/typeseval`**: those internal packages also export legitimate non-INV-1 helpers (`EachInSubtree`, `EachInChildren`, `ResolvePackageRef`, `ResolveMethodCall`, `EvaluateConstString`) that archtest authors should use directly. Path-level banning would force every legitimate user of those helpers to migrate to a façade wrapper or be exempted — bloat without security gain. Symbol-level banning of `EachFile` / `LoadPackages` / `SharedResolver` is the precise enforcement, and it requires `*types.Info` resolution which only the archtest layer (defense #3) provides. Lint stays narrow and Hard for the one path (`packages`) that is the load-bearing INV-1 reconstruction primitive.

**Cross-validation of allowlist drift**: `TestPassFunnelGuardListSync` archtest parses `.golangci.yml` at test time and asserts (a) every depguard negative-glob entry is a real archtest file present in `archtestmeta.LegacyAllowlist` or is `pass_funnel_test.go`, (b) every file directly importing `golang.org/x/tools/go/packages` is in the depguard negative-glob list. Manual synchronization drift is fail-loud at archtest-run time, not at PR-review attention.

`pass_funnel_test.go` is **permanently** exempt from defense #2 and skips itself in defense #3 (basename `pass_funnel_test.go`). The rule implementation must import the very entry points it forbids; the type system cannot tell rule implementation from rule violator.

### AI-rebust honest caveats

Defense #1 is the only compile-time Hard. Defenses #2 and #3 are review-detectable Hards — the rule cannot police modifications to its own configuration / allowlist. The combined three-layer design accepts that elementary meta-governance boundary: the AI-rebust charter §"meta-governance" notes that "fault-redundant defenses in production code are over-engineering, in meta-governance are correct" — every additional meta layer has diminishing return because reviewers must always backstop the topmost layer.

## Industry precedent

| Project | Pass shape | INV-1 defense |
|---------|-----------|---------------|
| **go/analysis** | `analysis.Pass` exposes `Files / TypesInfo / Fset / Pkg`; driver-private construction in `checker/checker.go`. **`Pass.Pkg` is `*types.Package`**, not `*packages.Package`. | Users author `Analyzer.Run(pass)`; cannot construct Pass freely. |
| **staticcheck** | Reuses `analysis.Pass`; helper functions `IsCallTo(pass, …)` take `*Pass`, not standalone `*types.Info`. | Same as go/analysis. |
| **golangci-lint** | Reuses `analysis.Pass` via the runner. | Same as go/analysis. |
| **wire** | `*gen{ pkg *packages.Package }` single field of truth; every method accesses AST + TypesInfo through that one `pkg`. | Single source by struct shape. |
| **ArchUnit (Java)** | `JavaClasses` physical representation (.class bytecode) — the dual-view problem is eliminated at the input layer. | N/A in Java. |

The `*types.Package` (not `*packages.Package`) shape is the **load-bearing** detail: it is what makes `pass.Pkg.Syntax` a compile error rather than a runtime check.

## Migration path (four stages)

Strategic plan: `docs/plans/202605141519-040-archtest-pass-funnel-plan.md`. Summary:

- **Stage 1** (this PR, refactor/574): Land the Pass framework + 3 Hard defenses + LegacyAllowlist of 53 existing archtests. Zero business archtest changes; the new framework coexists with the legacy entry points behind allowlist exemption.
- **Stages 2 / 3** (~9 PRs, parallelisable): Migrate the 53 legacy archtests theme-by-theme; each migration PR is the **first and only** edit to its target archtest (import + API + semantics in one commit), removes one entry from `LegacyAllowlist` AND the matching negative-glob in `.golangci.yml`.
- **Stage 4**: Delete `archtestmeta` package entirely; delete the negative-glob exemption block from `.golangci.yml`; delete the LegacyAllowlist reference from `pass_funnel_test.go`. Defense #1 (`Pass.Pkg` shape) and defense #2 (depguard deny list) remain permanent; defense #3's self-exemption (basename `pass_funnel_test.go`) is the only remaining exemption.

## Termination criteria

The migration is complete when:

1. `archtestmeta.LegacyAllowlist` map is empty (asserted statically in stage-4 PR).
2. `.golangci.yml` `archtest-no-direct-packages-load.files` contains only the positive glob `**/tools/archtest/*_test.go` and the single permanent `!**/tools/archtest/pass_funnel_test.go` self-exemption. The depguard `deny` list reduces to a single entry: `golang.org/x/tools/go/packages`.
3. No production archtest `*_test.go` imports `internal/scanner` / `internal/typeseval` / `golang.org/x/tools/go/packages` directly (verified by depguard + PASS-FUNNEL meta-archtest).

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
| Loader / load primitive | `LoadPackages` `SharedResolver` `LoadProductionPackages` `Resolver` `ProductionResolver` `EachFileInPackage` | **Never re-exported** — reachable only via `RunTyped` (this is the funnel Hard defense body) |
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

## References

- `docs/plans/202605141519-040-archtest-pass-funnel-plan.md` — strategic plan with 4-stage migration and parallelism analysis.
- `.claude/rules/gocell/ai-collab.md` — AI-rebust charter (Hard / Medium / Soft grading, vehicle decision principles, archtest naming).
- `tools/archtest/scanner_framework_usage_test.go` — `SCANNER-FRAMEWORK-USAGE-01` (sibling Hard meta-archtest; structural template for `pass_funnel_test.go`).
- `go/analysis.Pass` — upstream Pass shape ([pkg.go.dev/golang.org/x/tools/go/analysis](https://pkg.go.dev/golang.org/x/tools/go/analysis)).
- `docs/architecture/202605120000-adr-archtest-process-isolation.md` — `hack/verify-archtest.sh` 16-shard CI infrastructure; pass_funnel_test.go is discovered automatically.

ref: golang.org/x/tools `analysis.Pass.Pkg = *types.Package`; uber-go/wire `gen.pkg` single field of truth.
