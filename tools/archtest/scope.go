// Package archtest is the single-entry façade for GoCell architectural tests.
// Authors write rules as [Rule] closures and dispatch them via [Run] (AST-only)
// or [RunTyped] (with go/types info). Direct access to the underlying
// internal/scanner and internal/typeseval primitives is forbidden by the
// PASS-FUNNEL-* meta-archtest and the depguard archtest-no-direct-packages-load
// rule — both wired up in this same PR (574 / archtest pass-funnel PR-1).
//
// Hard-line defense layering:
//
//  1. [Pass.Pkg] is [*types.Package] (go/types stdlib), NOT [*packages.Package]
//     (golang.org/x/tools/go/packages). Authors that receive a *Pass cannot
//     reach .Syntax + an out-of-scope *types.Info pair — the INV-1 root cause
//     class becomes inexpressible at the compile level.
//  2. depguard rule archtest-no-direct-packages-load bans tools/archtest/*_test.go
//     from importing packages, internal/scanner, internal/typeseval.
//  3. Meta-archtest PASS-FUNNEL-EACHFILE-01 / LOADPACKAGES-01 / PACKAGES-IMPORT-01
//     re-detect any bypass via type-aware *types.Info resolution.
//
// See docs/architecture/202605141519-adr-archtest-pass-funnel.md for the full
// rationale and migration path.
package archtest

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Scope is the opaque file-set descriptor produced by [ModuleScope] or
// [DirsScope]. Zero value is invalid.
type Scope = scanner.Scope

// ScopeOption is the functional-option type accepted by [ModuleScope] /
// [DirsScope]; obtain values via [IncludeTests], [ExcludeRels], [MatchRels],
// [IncludeTestdata], [IncludeGenerated].
type ScopeOption = scanner.Option

// FileContext is the per-file payload supplied to AST-only callbacks (see
// internal scanner FileContext). External rules should generally prefer the
// [Pass]-driven API; FileContext is exposed for the small set of rules that
// still iterate files one-at-a-time via [Run].
type FileContext = scanner.FileContext

// Diagnostic represents a single rule violation. Rules accumulate Diagnostics
// in their return slice; the caller passes the slice to [Report] together with
// the rule ID.
type Diagnostic = scanner.Diagnostic

// ModuleScope creates a Scope rooted at modRoot that walks the entire module,
// skipping the default directory set: vendor, testdata, worktrees, generated,
// .git, node_modules.
func ModuleScope(modRoot string, opts ...ScopeOption) Scope {
	return scanner.ModuleScope(modRoot, opts...)
}

// DirsScope creates a Scope limited to dirs (relative to modRoot). See
// scanner.DirsScope for the full contract.
func DirsScope(modRoot string, dirs []string, opts ...ScopeOption) Scope {
	return scanner.DirsScope(modRoot, dirs, opts...)
}

// IncludeTests returns a ScopeOption that includes *_test.go files in the
// scope's file set.
func IncludeTests() ScopeOption { return scanner.IncludeTests() }

// ExcludeRels returns a ScopeOption that excludes specific module-relative
// file paths from the scope's file set. Slash-separated; post-Clean matching.
func ExcludeRels(rels ...string) ScopeOption { return scanner.ExcludeRels(rels...) }

// MatchRels returns a ScopeOption that retains only files whose module-relative
// slash path satisfies pred. Composes AND with default skip + ExcludeRels.
func MatchRels(pred func(rel string) bool) ScopeOption { return scanner.MatchRels(pred) }

// IncludeTestdata allows the walk to descend into "testdata" directories.
// Legal only when paired with DirsScope dirs containing a "testdata" segment.
func IncludeTestdata() ScopeOption { return scanner.IncludeTestdata() }

// IncludeGenerated allows the walk to descend into "generated" directories.
// Use for repo-wide rules whose invariant must also bind codegen output.
func IncludeGenerated() ScopeOption { return scanner.IncludeGenerated() }

// Report formats and emits each diagnostic as a t.Errorf call, sorted and
// deduplicated. An empty diags slice is a no-op. ruleID is prefixed to every
// emitted message: "<ruleID>: <rel>:<line>: <message>".
func Report(t *testing.T, ruleID string, diags []Diagnostic) {
	t.Helper()
	scanner.Report(t, ruleID, diags)
}
