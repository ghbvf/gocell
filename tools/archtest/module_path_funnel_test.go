package archtest

// INVARIANT: ARCHTEST-MODULE-PATH-FUNNEL-01
//
// module_path_funnel_test.go — the migration-convergence ratchet for M3
// (docs/architecture/202605281200-adr-cell-development-external-repo.md §R1/M3).
//
// Goal: make every archtest rule module-path-agnostic so an external Cell repo
// can run it against its own module. A rule's reference to a GoCell PLATFORM
// symbol path must be derived from the single sanctioned const
// [PlatformModulePath] (external.go) — NOT a bare "github.com/ghbvf/gocell…"
// string literal — so (a) a module rename updates exactly one place and (b)
// "no rule hardcodes the platform path" becomes machine-checkable.
//
// This ratchet records the CURRENT set of files that still contain a bare
// platform-path literal (the migration backlog) in a checked-in golden, and
// fails if the set ever changes without updating the golden:
//
//   - A NEW file introducing a bare literal → not in golden → CI red
//     (regression: a new rule hardcoded the path instead of using PlatformModulePath).
//   - A migrated file (literal removed) still listed in golden → CI red,
//     forcing the author to shrink the golden (the ratchet-down step).
//
// As rules migrate (M3 PR-2..N) the golden shrinks monotonically; when it
// reaches empty the funnel becomes a pure ban (no file outside external.go may
// contain the literal) — the Hard terminal state. This is ArchUnit's
// FreezingArchRule / ViolationStore pattern: freeze the current violations,
// fail only on new ones, shrink as they are fixed.
// ref: ArchUnit FreezingArchRule (TextFileBasedViolationStore, fail-on-new,
// auto-shrink-on-fix).
//
// # AI-robust rating (funnel double-lock)
//
//   - Downstream Hard: a bare "github.com/ghbvf/gocell[/…]" STRING literal is
//     detected by AST form (BasicLit prefix match). The SANCTIONED escape —
//     PlatformModulePath + "/pkg/x" — is by construction invisible to this
//     check (its BasicLit fragment is "/pkg/x", which does not start with the
//     path), so the migrated form is never a false positive and the bare form
//     is always caught. Form-uniqueness, no string-anchor / comment-allowlist.
//   - Upstream Medium: Go cannot prevent a package-internal author from writing
//     a bare string literal; the golden baseline is the archtest backstop. The
//     Hard upgrade is the terminal empty-golden state (pure ban), reached when
//     migration completes (#1302) — Hard-ization tracked by #1304. Until then
//     this is an explicit Medium-upstream/Hard-downstream transition funnel.
//
// # Blind spots (declared) + reverse self-checks
//
//	(a) Fragment-split literal: "github.com/ghbvf/" + "gocell/pkg/x" — neither
//	    BasicLit fragment starts with the full path, so the per-literal scan
//	    misses it. The reverse self-check below (TestArchtestModulePathFunnel/
//	    no-fragment-split) catches the two-literal BinaryExpr(+) form. Deeper
//	    N-fragment splitting is a documented residual blind spot; the
//	    EvaluateConstString-based Hard upgrade is NOT usable here because it
//	    would also flag the sanctioned PlatformModulePath+"/x" form. Residual
//	    risk is low (an author splitting a literal across 3+ pieces to evade a
//	    migration ratchet is implausible and would not survive review).
//	(b) Build-tagged files (//go:build x): Run is AST-only and parses every
//	    file regardless of build tags, so tagged rule files ARE scanned — not a
//	    blind spot here (unlike type-loading rules).

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const barePlatformLiteralMsg = "bare \"" + PlatformModulePath +
	"\" string literal — derive platform symbol paths from PlatformModulePath " +
	"(see external.go / ARCHTEST-MODULE-PATH-FUNNEL-01)"

// modulePathFunnelSanctioned is the one file permitted to contain the bare
// platform-path literal: external.go, which declares PlatformModulePath.
const modulePathFunnelSanctioned = "tools/archtest/external.go"

// TestArchtestModulePathFunnel enforces ARCHTEST-MODULE-PATH-FUNNEL-01: the set
// of tools/archtest files containing a bare PlatformModulePath literal must
// equal the checked-in golden (the migration backlog). Regenerate with:
//
//	go test ./tools/archtest/ -run '^TestArchtestModulePathFunnel$' -update
//
// and review the golden diff (it should only ever shrink).
func TestArchtestModulePathFunnel(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	// IncludeTests: today's rule logic lives in *_test.go; the funnel must see
	// it. testdata/ is skipped by DirsScope's default skip set; internal/ is
	// excluded below (loader primitives, out of the rule-authoring surface).
	scope := DirsScope(root, []string{"tools/archtest"}, IncludeTests())

	offenders := make(map[string]bool)
	_ = Run(t, scope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if !strings.HasPrefix(rel, "tools/archtest/") {
				continue
			}
			if strings.HasPrefix(rel, "tools/archtest/internal/") {
				continue
			}
			if strings.Contains(rel, "/testdata/") {
				continue
			}
			if rel == modulePathFunnelSanctioned {
				continue
			}
			if fileHasBarePlatformLiteral(f) {
				offenders[rel] = true
			}
		}
		return nil
	})

	diags := make([]Diagnostic, 0, len(offenders))
	for rel := range offenders {
		diags = append(diags, Diagnostic{Rel: rel, Line: 0, Message: barePlatformLiteralMsg})
	}
	goldenPath := filepath.Join(root, "tools", "archtest", "testdata", "module_path_funnel.golden")
	AssertGolden(t, goldenPath, diags)

	// Reverse self-check for declared blind spot (a): a two-literal
	// concatenation that reconstructs the platform path would evade the
	// per-literal scan. Assert no archtest file (outside the sanctioned site)
	// builds the path via BinaryExpr(+) of two string literals.
	t.Run("no-fragment-split", func(t *testing.T) {
		var splits []string
		_ = Run(t, scope, func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasPrefix(rel, "tools/archtest/") ||
					strings.HasPrefix(rel, "tools/archtest/internal/") ||
					strings.Contains(rel, "/testdata/") ||
					rel == modulePathFunnelSanctioned {
					continue
				}
				EachInSubtree[ast.BinaryExpr](f, func(be *ast.BinaryExpr) {
					if be.Op != token.ADD {
						return
					}
					x, xok := stringLitVal(be.X)
					y, yok := stringLitVal(be.Y)
					if !xok || !yok {
						return
					}
					if strings.HasPrefix(x+y, PlatformModulePath) {
						splits = append(splits, rel)
					}
				})
			}
			return nil
		})
		if len(splits) > 0 {
			t.Errorf("ARCHTEST-MODULE-PATH-FUNNEL-01: fragment-split platform literal in %v "+
				"(use PlatformModulePath)", splits)
		}
	})
}

// fileHasBarePlatformLiteral reports whether f contains a STRING literal (not an
// import-spec path) whose unquoted value is, or is a child path of,
// PlatformModulePath. Import-spec paths are excluded because importing a GoCell
// package is normal Go, not the hardcoding this funnel targets; the sanctioned
// PlatformModulePath+"/x" form is invisible here because its fragment literal
// is "/x" (no platform prefix).
func fileHasBarePlatformLiteral(f *ast.File) bool {
	importLits := make(map[*ast.BasicLit]bool, len(f.Imports))
	for _, imp := range f.Imports {
		if imp.Path != nil {
			importLits[imp.Path] = true
		}
	}
	found := false
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if found || lit.Kind != token.STRING || importLits[lit] {
			return
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		if val == PlatformModulePath || strings.HasPrefix(val, PlatformModulePath+"/") {
			found = true
		}
	})
	return found
}

// stringLitVal returns the unquoted value of a STRING basic literal expression.
func stringLitVal(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}
