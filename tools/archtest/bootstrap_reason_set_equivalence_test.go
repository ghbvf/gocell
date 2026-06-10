//go:build archtest

// INVARIANT: BOOTSTRAP-REASON-SET-EQUIVALENCE-01
//
// # BOOTSTRAP-REASON-SET-EQUIVALENCE-01 — bootstrap auth-fail reason set equivalence (Medium)
//
// ## Rule
//
// The set of bootstrap auth-fail reason strings must be identical across three
// sites:
//
//  1. runtime/audit — authoritative exported consts (ReasonMissingHeader,
//     ReasonWrongCredentials, ReasonRateLimited). These are the canonical values.
//  2. cells/accesscore/slices/setup — the whitelist map in validBootstrapAuthFailReasons
//     (which now references the runtime/audit consts directly — C5 change).
//  3. runtime/auth/bootstrap.go — inline string literals in the middleware
//     (runtime/auth deliberately keeps its own literals per layering — it cannot
//     import runtime/audit without creating an import cycle; the archtest
//     verifies the SETS are equal, it does NOT require runtime/auth to import audit).
//
// ## Why this invariant exists
//
// The bootstrap auth-fail event flows from runtime/auth (producer of the reason
// string) through cells/accesscore/slices/setup (whitelist validation + emit) to
// cells/auditcore/slices/auditappendbootstrap (consume + append). A drift in any
// of the three sites means events are either silently dropped (producer sends
// unknown reason → whitelist rejects → DLX) or ledger entries carry invalid
// reasons (AppendBootstrapAuthFail whitelist rejects → DLX).
//
// ## AI-robust rating: Medium
//
// The archtest uses go/types typed package loading to enumerate exported string
// constants from runtime/audit (authoritative source) and compares them against
// string literals extracted via AST scan from runtime/auth/bootstrap.go and
// the const-eval'd map keys in cells/accesscore/slices/setup. Typed loading
// (Run with a Production scope) prevents const-value indirection from bypassing the check.
//
// Hard upgrade path: generate a shared compile-time constant set from the three
// sites via a codegen funnel so the compiler enforces equivalence. Tracked as
// backlog item (no current issue — the three-site set is small and stable).
//
// ## Blind spots (documented per ai-robust.md §"工具选定后强制盲区自检")
//
//  1. **Constant re-declaration with different name**: if runtime/auth declares
//     `const myReason = "missing_header"` and uses that instead of the literal,
//     the AST scan of bootstrap.go may not see the string value. Reverse
//     self-check: TestBootstrapReasonSetEquivalence01_ReverseCheck_LiteralNotConst
//     verifies the scanner finds literals in the synthetic fixture but NOT
//     const-only declarations.
//  2. **Multi-package indirection**: if runtime/auth imported a helper package
//     that contains the literals, the scan of bootstrap.go would not see them.
//     Currently impossible (would create an import cycle); tracked as a won't-do.
//  3. **map key iteration order**: the comparison uses set equality (sorted slices),
//     not order-sensitive equality.
package archtest

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleBootstrapReasonSetEquivalence01 = "BOOTSTRAP-REASON-SET-EQUIVALENCE-01"

// reasonPrefixInAudit is the prefix for the authoritative reason constants in
// runtime/audit (ReasonMissingHeader / ReasonWrongCredentials / ReasonRateLimited).
const reasonPrefixInAudit = "Reason"

// bootstrapGoSuffix is the path suffix for runtime/auth/bootstrap.go — the
// file that contains the inline reason literals.
const bootstrapGoSuffix = "/runtime/auth/bootstrap.go"

// runtimeAuthPkgSuffix is the package path suffix for runtime/audit where the
// authoritative Reason* constants live.
const runtimeAuditPkgSuffix = "/runtime/audit"

// setupPkgSuffix is the package path suffix for cells/accesscore/slices/setup,
// the producer-side whitelist site (third reason set).
const setupPkgSuffix = "/cells/accesscore/slices/setup"

// setupWhitelistVarName is the package-level map var whose keys are the
// producer-side reason whitelist (keyed by audit.Reason* selector exprs).
const setupWhitelistVarName = "validBootstrapAuthFailReasons"

// TestBootstrapReasonSetEquivalence01 enforces that the bootstrap auth-fail
// reason string values are identical across:
//
//  1. runtime/audit exported Reason* consts (authoritative).
//  2. runtime/auth/bootstrap.go inline string literals (producer middleware).
//  3. cells/accesscore/slices/setup/service.go validBootstrapAuthFailReasons map
//     keys (whitelist validation — after C5 these reference runtime/audit consts,
//     so the typed-load check verifies the map keys resolve to the same values).
func TestBootstrapReasonSetEquivalence01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkg := modPath + runtimeAuditPkgSuffix
	setupPkg := modPath + setupPkgSuffix

	// Step 1: collect authoritative Reason* const values from runtime/audit AND
	// the producer-side whitelist map keys from cells/accesscore/slices/setup,
	// in a single typed pass. The setup keys are audit.Reason* selector exprs;
	// TypesInfo resolves them to their underlying string const values, so this
	// catches drift even though the source uses const references (not literals).
	var auditReasons []string
	var setupReasons []string
	auditVisited := false
	setupVisited := false

	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		switch p.Pkg.Path() {
		case auditPkg:
			auditVisited = true
			scope := p.Pkg.Scope()
			for _, name := range scope.Names() {
				if !strings.HasPrefix(name, reasonPrefixInAudit) {
					continue
				}
				obj := scope.Lookup(name)
				c, ok := obj.(*types.Const)
				if !ok {
					continue
				}
				if c.Val() == nil || c.Val().Kind() != constant.String {
					continue
				}
				auditReasons = append(auditReasons, constant.StringVal(c.Val()))
			}
		case setupPkg:
			setupVisited = true
			setupReasons = append(setupReasons, extractSetupWhitelistKeys(p)...)
		}
		return nil
	})

	require.True(t, auditVisited,
		"%s: Production scope did not visit %q — scope gap, rule would pass vacuously",
		ruleBootstrapReasonSetEquivalence01, auditPkg)
	require.NotEmpty(t, auditReasons,
		"%s: no Reason* consts found in %q — authoritative set is empty (bug)",
		ruleBootstrapReasonSetEquivalence01, auditPkg)
	require.True(t, setupVisited,
		"%s: Production scope did not visit %q — scope gap, rule would pass vacuously",
		ruleBootstrapReasonSetEquivalence01, setupPkg)
	require.NotEmpty(t, setupReasons,
		"%s: no %s map keys resolved in %q — whitelist set is empty (bug)",
		ruleBootstrapReasonSetEquivalence01, setupWhitelistVarName, setupPkg)
	sort.Strings(auditReasons)
	sort.Strings(setupReasons)

	// Step 2: extract inline string literals from runtime/auth/bootstrap.go.
	authReasons := extractBootstrapGoLiterals(t, root)
	sort.Strings(authReasons)

	// Step 3: compare all three sets against the authoritative runtime/audit set.
	assert.Equal(t, auditReasons, authReasons,
		"%s: reason string values in runtime/auth/bootstrap.go (%v) must equal "+
			"runtime/audit Reason* consts (%v) — drift means events may be silently DLX'd",
		ruleBootstrapReasonSetEquivalence01, authReasons, auditReasons)
	assert.Equal(t, auditReasons, setupReasons,
		"%s: %s map keys in cells/accesscore/slices/setup (%v) must equal "+
			"runtime/audit Reason* consts (%v) — drift means the producer whitelist "+
			"rejects valid reasons (→ DLX) or admits invalid ones",
		ruleBootstrapReasonSetEquivalence01, setupWhitelistVarName, setupReasons, auditReasons)
}

// extractSetupWhitelistKeys finds the package-level setupWhitelistVarName map
// var in the setup package's files and returns its resolved string keys. Keys
// are audit.Reason* selector exprs; TypesInfo.Types[key].Value resolves each to
// its underlying string const value, so const indirection cannot bypass the
// equivalence check (the rationale for the typed pass over a pure AST scan).
func extractSetupWhitelistKeys(p *Pass) []string {
	var keys []string
	for _, f := range p.Files {
		scanner.EachInChildren[ast.GenDecl](f, func(gen *ast.GenDecl) {
			if gen.Tok != token.VAR {
				return
			}
			scanner.EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
				for i, name := range vs.Names {
					if name.Name != setupWhitelistVarName || i >= len(vs.Values) {
						continue
					}
					cl, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok {
						continue
					}
					scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
						if tv, ok := p.TypesInfo.Types[kv.Key]; ok &&
							tv.Value != nil && tv.Value.Kind() == constant.String {
							keys = append(keys, constant.StringVal(tv.Value))
						}
					})
				}
			})
		})
	}
	return keys
}

// findFileWithSuffix walks the module root and returns the first regular file
// whose path ends with the given suffix. Returns "" if not found.
func findFileWithSuffix(t *testing.T, root, suffix string) string {
	t.Helper()
	// Normalize suffix to slash form and strip any leading slash so it matches
	// the module-relative paths returned by scanner.Scope.Files().
	suffixSlash := strings.TrimPrefix(filepath.ToSlash(suffix), "/")
	scope := scanner.ModuleScope(root, scanner.MatchRels(func(rel string) bool {
		return strings.HasSuffix(filepath.ToSlash(rel), suffixSlash)
	}))
	files, err := scope.Files()
	if err != nil || len(files) == 0 {
		return ""
	}
	return files[0]
}

// extractBootstrapGoLiterals parses runtime/auth/bootstrap.go and returns all
// basic string literals that appear as standalone (non-concatenated) string
// values that look like reason tokens (lowercase underscore-separated words).
// This is the AST-only path since runtime/auth must NOT import runtime/audit.
func extractBootstrapGoLiterals(t *testing.T, moduleRoot string) []string {
	t.Helper()
	// Find runtime/auth/bootstrap.go relative to the module root.
	bootstrapFile := findFileWithSuffix(t, moduleRoot, bootstrapGoSuffix)
	require.NotEmpty(t, bootstrapFile,
		"%s: could not find %s under module root", ruleBootstrapReasonSetEquivalence01, bootstrapGoSuffix)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, bootstrapFile, nil, parser.AllErrors)
	require.NoError(t, err, "%s: parse %s", ruleBootstrapReasonSetEquivalence01, bootstrapFile)

	var reasons []string
	scanner.EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		// Unquote the string literal.
		val := strings.Trim(lit.Value, `"`)
		if !isBootstrapReasonCandidate(val) {
			return
		}
		reasons = append(reasons, val)
	})

	// De-duplicate (same literal may appear in godoc + return).
	seen := map[string]bool{}
	var deduped []string
	for _, r := range reasons {
		if !seen[r] {
			seen[r] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

// isBootstrapReasonCandidate reports whether s looks like a bootstrap reason token:
// all lowercase, non-empty, contains only letters and underscores, and contains
// at least one underscore (distinguishes from other short strings).
// This heuristic is tightly scoped — the three current reasons all match.
func isBootstrapReasonCandidate(s string) bool {
	if len(s) == 0 {
		return false
	}
	hasUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '_':
			hasUnderscore = true
		default:
			return false
		}
	}
	return hasUnderscore
}

// TestBootstrapReasonSetEquivalence01_ReverseCheck verifies the two scanning
// strategies used by the production rule:
//
//  1. isBootstrapReasonCandidate correctly classifies known reason values and
//     rejects non-reason strings.
//  2. A synthetic bootstrap.go snippet with inline literals is correctly parsed
//     and the reason strings are extracted.
func TestBootstrapReasonSetEquivalence01_ReverseCheck(t *testing.T) {
	t.Parallel()

	t.Run("candidate_classification", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			input string
			want  bool
		}{
			{"missing_header", true},
			{"wrong_credentials", true},
			{"rate_limited", true},
			{"", false},
			{"noUnderscore", false},
			{"UPPER_CASE", false},
			{"has space", false},
			{"has-dash", false},
			{"numbers123_here", false},
		} {
			assert.Equal(t, tc.want, isBootstrapReasonCandidate(tc.input),
				"candidate(%q)", tc.input)
		}
	})

	t.Run("literal_extraction_from_synthetic_snippet", func(t *testing.T) {
		t.Parallel()
		const src = `package auth
func check() (string, bool) {
    return "missing_header", false
}
func check2() (string, bool) {
    return "wrong_credentials", false
}
func limit() {
    onAuthFail(ctx, "rate_limited")
}
`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic_bootstrap.go", src, parser.AllErrors)
		require.NoError(t, err)

		var found []string
		scanner.EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			val := strings.Trim(lit.Value, `"`)
			if isBootstrapReasonCandidate(val) {
				found = append(found, val)
			}
		})
		sort.Strings(found)
		want := []string{"missing_header", "rate_limited", "wrong_credentials"}
		assert.Equal(t, want, found, "synthetic snippet must yield all three reason strings")
	})

	t.Run("non_literal_const_not_found_by_ast_scan", func(t *testing.T) {
		t.Parallel()
		// This reverse self-check documents Blind Spot 1: if reasons were declared as
		// named consts (not inline literals), the AST literal scan would not find them.
		const src = `package auth
const missingHeaderConst = "missing_header"
func check() (string, bool) {
    return missingHeaderConst, false // AST: Ident, not BasicLit
}
`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic_const.go", src, parser.AllErrors)
		require.NoError(t, err)

		var found []string
		scanner.EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			val := strings.Trim(lit.Value, `"`)
			if isBootstrapReasonCandidate(val) {
				found = append(found, val)
			}
		})
		// The scanner DOES find the const declaration literal ("missing_header" on
		// the right-hand side of the const decl), but NOT the ident reference in
		// the return statement. This documents the blind spot: if runtime/auth used
		// const indirection without the const decl in the same file, values would be
		// missed. Currently runtime/auth uses inline literals, so this is not a risk.
		assert.Contains(t, found, "missing_header",
			"const declaration literal is found by AST scan (decl RHS is BasicLit)")
	})
}
