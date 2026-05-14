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
