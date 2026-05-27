// invariants asserted in this file:
//   - INVARIANT: LOCATOR-DISCOVERY-FUNNEL-01
//
// LOCATOR-DISCOVERY-FUNNEL-01 (M1 of #1082) keeps kernel/metadata.Locator the
// sole place in the codebase that knows how the filesystem topology maps to
// metadata YAML buckets. It is a two-direction funnel:
//
//   A1 (upstream Medium — Go-language final ceiling): all fs.WalkDir /
//      filepath.Walk / fs.ReadDir callsites under kernel/metadata/** must
//      live inside Locator.discoverConventional / discoverManifest (and the
//      manifest glob expansion helpers that are themselves part of the
//      funnel). The package-internal bypass is bounded only by this
//      archtest's caller-allowlist; sealed cross-package interface is
//      structurally unreachable in Go (SPAN-SETATTR-HOLDER-SEAL-01 #851
//      same precedent).
//
//   A2 (downstream Hard — form-uniqueness): path-prefix string comparisons
//      against the conventional layout tokens {cells/, contracts/, journeys/,
//      assemblies/, examples/} are restricted to the locator's own files in
//      kernel/metadata/locator*.go (plus two governance helper files for
//      conventional-mode strict rules). The form catalog locks two AST
//      shapes today and a reverse blind-spot self-check rejects two
//      additional shapes via negative tests.
//
// AI-robust grading: Medium upstream + Hard downstream is a final form per
// ai-robust §"Funnel 双向锁评级". No follow-up issue is opened; the upstream
// ceiling is a Go-language limit, not a deferred TODO. See ADR 202605281200.

package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// locatorLayoutTokens are the path-token literals whose presence in
// kernel/metadata/** or kernel/governance/** code (outside the locator
// files) indicates a leak of filesystem topology knowledge from Locator
// into downstream code.
var locatorLayoutTokens = []string{
	"cells",
	"contracts",
	"journeys",
	"assemblies",
	"examples",
}

// locatorLayoutPrefixes are the same tokens with a trailing slash, matched
// by strings.HasPrefix-style call shapes.
var locatorLayoutPrefixes = []string{
	"cells/",
	"contracts/",
	"journeys/",
	"assemblies/",
	"examples/",
}

// locatorFunnelFiles names the metadata files that are allowed to hold
// path-prefix literals and fs.WalkDir callsites. Any file outside this set
// in kernel/metadata/** or kernel/governance/** that contains a flagged
// shape fails the archtest.
var locatorFunnelFiles = map[string]bool{
	"locator.go":              true,
	"locator_conventional.go": true,
	"locator_manifest.go":     true,
	"locator_test.go":         true,
}

// locatorAllowedWalkCallers names the function bodies in which fs.WalkDir /
// filepath.Walk / fs.ReadDir may appear inside kernel/metadata/**. Anything
// else trips A1.
var locatorAllowedWalkCallers = map[string]bool{
	"discoverConventional":   true,
	"discoverManifest":       true,
	"discoverManifestModule": true,
	"matchManifestGlob":      true,
}

// locatorGovernanceConventionalFiles names governance files allowed to hold
// path-prefix literals because they implement conventional-layout-specific
// governance rules (e.g., FMT-21 validates that contract.Dir matches the
// derived "contracts/<id-as-path>" form). These are governance invariants
// for conventional-mode projects, not part of the Locator funnel boundary.
//
// Extending this allowlist must come with a written rationale in code
// review — the inclusion criterion is "the rule is intrinsically about
// conventional layout and would be incorrect under Manifest mode".
//
// governance/locator.go holds canonicalCellID / canonicalSliceKey /
// canonicalContractID / canonicalJourneyID / canonicalAssemblyID —
// conventional-layout-specific reverse mappers (path → canonical ID) used
// by governance ValidationResult dedup. Same rationale; the parse-side
// mapping is in kernel/metadata.Locator, this is its dual for governance.
var locatorGovernanceConventionalFiles = map[string]bool{
	"helpers.go":           true,
	"rules_misc_strict.go": true,
	"locator.go":           true,
}

// locatorScanDirs returns the directories whose Go files are scanned by
// the LOCATOR-DISCOVERY-FUNNEL-01 invariants. kernel/metadata houses the
// Locator funnel; kernel/governance houses TargetSelector and the
// conventional-layout strict rules.
func locatorScanDirs() []string {
	return []string{
		"kernel/metadata",
		"kernel/governance",
	}
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A1_WalkdirCallerAllowlist enforces that
// fs.WalkDir / filepath.Walk / fs.ReadDir are only called from within the
// Locator's discover{Conventional,Manifest} bodies (and the manifest glob
// expansion helpers that are themselves part of the funnel) inside
// kernel/metadata/**.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A1_WalkdirCallerAllowlist(t *testing.T) {
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, []string{"kernel/metadata"}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if fn.Body == nil || locatorAllowedWalkCallers[fn.Name.Name] {
						return
					}
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						name, ok := locatorWalkCalleeName(call)
						if !ok {
							return
						}
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: p.Fset.Position(call.Pos()).Line,
							Message: "A1 (Medium upstream): " + name +
								" is only allowed inside " + locatorJoinKeys(locatorAllowedWalkCallers) +
								" (callsite in " + fn.Name.Name + ")",
						})
					})
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A1", diags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A2a_HasPrefixFormUniqueness enforces that
// strings.HasPrefix(_, lit) call shapes with a conventional-layout-prefix
// literal as the second argument may only appear in the Locator's own
// files inside kernel/metadata/** and kernel/governance/**.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A2a_HasPrefixFormUniqueness(t *testing.T) {
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !locatorIsCalleeStringsHasPrefix(call) || len(call.Args) < 2 {
						return
					}
					lit, ok := locatorStringLiteral(call.Args[1])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "A2a (Hard downstream): strings.HasPrefix(_, " +
									strconv.Quote(lit) + ") may only appear inside the Locator funnel files",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A2a", diags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A2b_EqualityComparisonFormUniqueness
// enforces the A2b form: a binary expression `x == "cells"` (or any other
// conventional-layout token) inside kernel/metadata/** or
// kernel/governance/** must live in the Locator's own files. This is the
// shape used by path-segment matchers (`parts[N] == "cells"`).
func TestLOCATOR_DISCOVERY_FUNNEL_01_A2b_EqualityComparisonFormUniqueness(t *testing.T) {
	root := findModuleRoot(t)
	diags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.BinaryExpr](f, func(bin *ast.BinaryExpr) {
					if bin.Op != token.EQL && bin.Op != token.NEQ {
						return
					}
					lit, ok := locatorStringLiteral(bin.Y)
					if !ok {
						lit, ok = locatorStringLiteral(bin.X)
						if !ok {
							return
						}
					}
					for _, banned := range locatorLayoutTokens {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(bin.Pos()).Line,
								Message: "A2b (Hard downstream): <expr> ==/!= " +
									strconv.Quote(lit) +
									" may only appear inside the Locator funnel files",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A2b", diags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_BlindSpotInventory documents the AST
// shapes that the A2 form catalog does NOT explicitly lock and asserts
// they remain absent in production code (reverse blind-spot self-check
// required by ai-robust §"工具选定后强制盲区自检").
//
// Today the catalog covers:
//   - A2a: strings.HasPrefix(_, "<token>/")
//   - A2b: x == "<token>" or x != "<token>"
//
// Not directly locked (blind spots), enforced as negative invariants
// in this test function:
//   - String concatenation rebuilding a forbidden literal (e.g.
//     "cell" + "s/")
//   - Regex anchors (regexp.MustCompile(`^cells/`))
//   - filepath.Join("<token>", ...) reconstructing a layout path
//   - strings.Contains(_, "<token>/") substring scanning
//
// Upstream Hard is a Go-language won't-do (sealed cross-package
// interface with private constructor is structurally unreachable in
// Go) — see SPAN-SETATTR-HOLDER-SEAL-01 #851 same precedent; no
// tracking issue is opened for the Medium upstream ceiling.
func TestLOCATOR_DISCOVERY_FUNNEL_01_BlindSpotInventory(t *testing.T) {
	root := findModuleRoot(t)
	concatDiags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.BinaryExpr](f, func(bin *ast.BinaryExpr) {
					if bin.Op != token.ADD {
						return
					}
					lhs, lok := locatorStringLiteral(bin.X)
					rhs, rok := locatorStringLiteral(bin.Y)
					if !lok || !rok {
						return
					}
					joined := lhs + rhs
					for _, banned := range locatorLayoutPrefixes {
						if joined == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(bin.Pos()).Line,
								Message: "blind-spot #1 (concat): <literal>+<literal> rebuilds " +
									strconv.Quote(banned),
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.CONCAT", concatDiags)

	regexDiags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if len(call.Args) < 1 {
						return
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "regexp" {
						return
					}
					if sel.Sel.Name != "MustCompile" && sel.Sel.Name != "Compile" {
						return
					}
					lit, ok := locatorStringLiteral(call.Args[0])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if strings.HasPrefix(lit, "^"+banned) {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "blind-spot #2 (regex): regexp.MustCompile(`^" +
									banned + "...`)",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.REGEX", regexDiags)

	// Blind-spot #3: filepath.Join("<token>", ...) — reconstructs a
	// conventional-layout path without any HasPrefix or ==/!= shape.
	// Originally caught a `filepath.Join("cells", ...)` leak in
	// kernel/governance/rules_misc_consistency.go after the M1 refactor.
	joinDiags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if len(call.Args) < 1 {
						return
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok {
						return
					}
					if (ident.Name != "filepath" && ident.Name != "path") || sel.Sel.Name != "Join" {
						return
					}
					lit, ok := locatorStringLiteral(call.Args[0])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutTokens {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "blind-spot #3 (filepath.Join): " + ident.Name + ".Join(" +
									strconv.Quote(lit) + ", ...) reconstructs a conventional-layout path",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.JOIN", joinDiags)

	// Blind-spot #4: strings.Contains(_, "<token>/") — substring scan for
	// a conventional-layout prefix. Not caught by A2a (HasPrefix) because
	// the callee differs.
	containsDiags := Run(t, DirsScope(root, locatorScanDirs()),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if len(call.Args) < 2 {
						return
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "strings" || sel.Sel.Name != "Contains" {
						return
					}
					lit, ok := locatorStringLiteral(call.Args[1])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "blind-spot #4 (strings.Contains): strings.Contains(_, " +
									strconv.Quote(lit) + ") substring-scans a conventional-layout prefix",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.CONTAINS", containsDiags)
}

// --- internal helpers ---

// locatorWalkCalleeName returns the name (e.g. "fs.WalkDir") of a call
// expression when the call matches one of the walk-style fs helpers tracked
// by A1.
func locatorWalkCalleeName(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch {
	case pkgIdent.Name == "fs" && (sel.Sel.Name == "WalkDir" || sel.Sel.Name == "ReadDir"):
		return "fs." + sel.Sel.Name, true
	case pkgIdent.Name == "filepath" && (sel.Sel.Name == "Walk" || sel.Sel.Name == "WalkDir"):
		return "filepath." + sel.Sel.Name, true
	}
	return "", false
}

// locatorIsCalleeStringsHasPrefix reports whether the call expression is
// strings.HasPrefix(...).
func locatorIsCalleeStringsHasPrefix(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "strings" && sel.Sel.Name == "HasPrefix"
}

// locatorStringLiteral returns the unquoted value of expr if it is a basic
// string literal; returns ("", false) otherwise.
func locatorStringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	unq, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return unq, true
}

// locatorIsAllowedFile reports whether the module-relative path is one of
// the Locator funnel's allowed files (locator*.go in kernel/metadata/, or
// the two governance conventional-mode helper files), or a _test.go file.
func locatorIsAllowedFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	base := filepath.Base(rel)
	if strings.HasPrefix(rel, "kernel/metadata/") && locatorFunnelFiles[base] {
		return true
	}
	if strings.HasPrefix(rel, "kernel/governance/") && locatorGovernanceConventionalFiles[base] {
		return true
	}
	return false
}

func locatorJoinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
