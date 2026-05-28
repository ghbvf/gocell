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
//      shapes today (HasPrefix + == ) and uses EvaluateConstString (go/types
//      constant folding) so that cross-package const references (e.g.
//      const cellsPrefix = "cells/") cannot bypass the check.
//      A reverse blind-spot self-check rejects const-eval bypass forms via
//      negative tests.
//
//   A5 (downstream Hard — consumer-path reverse funnel): filepath.Join calls
//      whose arguments evaluate to conventional layout tokens ("cells", "cmd")
//      are banned from kernel/governance/**, cmd/gocell/**, and
//      kernel/metadata/** (outside the Locator funnel files). This closes
//      the EXITING side: governance and CLI code must not reconstruct
//      conventional paths; they must instead consume paths from the Locator
//      output (MetadataSource.File / CellMeta.File / etc.).
//      AI-robust grading: upstream Medium (archtest caller allowlist) +
//      downstream Hard (form uniqueness via const-eval arg scanning).
//      Funnel EXITING upstream is Medium (archtest caller-allowlist + blind-spot
//      self-check); upstream Hard 升级路径 (Locator sealed envelope /
//      typed pathx package / accept Medium 上限) is tracked by gh issue #1235.
//
// AI-robust grading: Medium upstream + Hard downstream is a final form per
// ai-robust §"Funnel 双向锁评级". No follow-up issue is opened for A1/A2; the
// upstream ceiling is a Go-language limit, not a deferred TODO. See ADR
// 202605281200. A5 upstream Hard tracking: gh issue #1235.

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
//
// kernel/metadata/locator.go also exports path-classification helpers
// (IsConventionalAssemblyPath, IsInExamplesSubtree) as legitimate funnel
// exit points — these are intentionally inside the funnel allowlist.
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
	// ctmCheckExamplesArrow uses strings.HasPrefix(_, "examples/") for conventional-layout
	// enforcement — intrinsically conventional, would be incorrect under Manifest mode.
	"rules_contract_test_mapping.go": true,
	"locator.go":                     true,
}

// locatorConsumerPathTokens are the conventional layout tokens whose presence
// as a literal argument to filepath.Join in consumer code (kernel/governance,
// cmd/gocell, kernel/metadata outside Locator funnel files) indicates a
// hardcoded path reconstruction that should instead consume the path from the
// Locator's output (e.g. CellMeta.File, SliceMeta.File).
var locatorConsumerPathTokens = []string{
	"cells",
	"cmd",
}

// locatorA5AllowedFiles names the module-relative source files that are
// permanently allowlisted from the A5 consumer-path check. Each entry must
// have a written rationale.
//
// Scope/allowlist principle: allowlist entries are **explicit exemptions**
// for write-paths (layout generators) or IsConventional*-guarded paths
// (files that emit layout tokens but are themselves guarded by a
// conventional-mode predicate). Files outside the A5 scan scope (e.g.
// testdata/, tools/) are simply not in the funnel's EXITING responsibility
// domain; they are not added here. The two categories must not be mixed.
//
//   - cmd/gocell/app/scaffold.go: `gocell scaffold cell|slice` is a
//     **write** path that intentionally produces the conventional
//     cells/<id>/cell.yaml layout. Manifest-mode users do not call scaffold
//     (manifest mode means "I own my layout"). A5 guards **read** paths
//     (governance / check / assembly_derive) where consuming layout tokens
//     indicates hardcoded discovery; scaffold is a layout generator by
//     definition.
//
//   - kernel/metadata/assembly_derive.go: the conventional assembly
//     entrypoint is cmd/<id>/main.go — "cmd" here is the physical output
//     directory name of the conventional layout, not a discovery token
//     derived from CellMeta/SliceMeta. The file contains an
//     isConventionalAssemblyPath guard so that Manifest-mode assemblies
//     (assemblies/<id>/ absent) use path.Dir(asm.File)/main.go instead.
//     See deriveAssembly comment for the full rationale.
//
//   - kernel/assembly/generator.go: the Generator is a **write** path that
//     produces the conventional assembly scaffold output — both
//     assemblies/<id>/assembly.yaml and cmd/<id>/run.go,main.go,app.go.
//     "assemblies" and "cmd" here are the physical output directory names
//     of the conventional scaffold layout, not discovery tokens consumed
//     from Locator/MetadataSource output. Same rationale as scaffold.go
//     (layout generator by definition, not a read/discovery consumer).
var locatorA5AllowedFiles = map[string]bool{
	"cmd/gocell/app/scaffold.go":         true,
	"kernel/metadata/assembly_derive.go": true,
	"kernel/assembly/generator.go":       true,
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

// locatorConsumerScanPatterns returns the Go package patterns for A5 (consumer-
// path reverse funnel). These are the packages outside the Locator core that
// must not reconstruct conventional layout paths via filepath.Join.
//
// kernel/assembly/ is included because the Generator writes output to
// conventional layout paths (assemblies/<id>/ and cmd/<id>/). Write-path
// files are allowlisted in locatorA5AllowedFiles (generator.go) with a
// written rationale; any other file in kernel/assembly/ that uses a banned
// token would indicate an unexpected read-path reconstruction and should
// fail A5.
func locatorConsumerScanPatterns() []string {
	return []string{
		"./kernel/governance/...",
		"./cmd/gocell/...",
		"./kernel/metadata/...",
		"./kernel/assembly/...",
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
//
// Uses EvaluateConstString (go/types constant folding) to resolve the second
// argument so that cross-package const references such as:
//
//	const cellsPrefix = "cells/"
//	strings.HasPrefix(p, cellsPrefix)
//
// are caught, not just bare BasicLit forms.
//
// Blind spots documented in TestLOCATOR_DISCOVERY_FUNNEL_01_BlindSpotInventory
// and A2_ConstEvalBypassBlindSpots.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A2a_HasPrefixFormUniqueness(t *testing.T) {
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
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
					lit, ok := EvaluateConstString(p.TypesInfo, call.Args[1])
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
//
// Uses EvaluateConstString (go/types constant folding) to resolve operands so
// that cross-package const references such as:
//
//	const cellsTok = "cells"
//	parts[0] == cellsTok
//
// are caught, not just bare BasicLit operands.
//
// Blind spots documented in TestLOCATOR_DISCOVERY_FUNNEL_01_BlindSpotInventory
// and A2_ConstEvalBypassBlindSpots.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A2b_EqualityComparisonFormUniqueness(t *testing.T) {
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
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
					lit, ok := EvaluateConstString(p.TypesInfo, bin.Y)
					if !ok {
						lit, ok = EvaluateConstString(p.TypesInfo, bin.X)
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
//   - strings.Replace / strings.ReplaceAll with a layout-token literal
//     (e.g. strings.Replace(p, "cells/", "", 1))
//   - strings.TrimPrefix(_, "<token>/") stripping a layout-token prefix
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

	// Blind-spot #5: strings.Replace(_, "<token>/", ...) and
	// strings.ReplaceAll(_, "<token>/", ...) — structural string mutation
	// using a conventional-layout prefix as the old-value argument. Not
	// caught by A2a (HasPrefix), A2b (==/!=), or blind-spots #1–#4.
	// Uses EvaluateConstString to resolve the second argument.
	replaceDiags := RunTyped(t, TypedOpts{Tests: false},
		[]string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "strings" {
						return
					}
					if sel.Sel.Name != "Replace" && sel.Sel.Name != "ReplaceAll" {
						return
					}
					// Replace: strings.Replace(s, old, new, n) — old is arg[1].
					// ReplaceAll: strings.ReplaceAll(s, old, new) — old is arg[1].
					if len(call.Args) < 2 {
						return
					}
					lit, ok := EvaluateConstString(p.TypesInfo, call.Args[1])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "blind-spot #5 (strings.Replace/ReplaceAll): " +
									sel.Sel.Name + "(_, " + strconv.Quote(lit) + ", ...) " +
									"mutates a conventional-layout prefix outside the Locator funnel",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.REPLACE", replaceDiags)

	// Blind-spot #6: strings.TrimPrefix(_, "<token>/") — strips a
	// conventional-layout prefix literal. Not caught by A2a (HasPrefix).
	// Uses EvaluateConstString to resolve the second argument.
	trimPrefixDiags := RunTyped(t, TypedOpts{Tests: false},
		[]string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if locatorIsAllowedFile(rel) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "strings" || sel.Sel.Name != "TrimPrefix" {
						return
					}
					if len(call.Args) < 2 {
						return
					}
					lit, ok := EvaluateConstString(p.TypesInfo, call.Args[1])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "blind-spot #6 (strings.TrimPrefix): " +
									"strings.TrimPrefix(_, " + strconv.Quote(lit) + ") " +
									"strips a conventional-layout prefix outside the Locator funnel",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.BLINDSPOT.TRIMPREFIX", trimPrefixDiags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A2_ConstEvalBypassBlindSpots asserts that
// the two forms that would bypass a BasicLit-only A2 check are absent from
// production code in kernel/metadata/** and kernel/governance/**:
//
//  1. Cross-package const used as HasPrefix argument:
//     const cellsPrefix = "cells/"
//     strings.HasPrefix(p, cellsPrefix)
//     → EvaluateConstString resolves cellsPrefix to "cells/" and catches it;
//     this test asserts it does NOT appear so A2a keeps its PASS status.
//
//  2. Cross-package const used as equality operand:
//     const cellsTok = "cells"
//     parts[0] == cellsTok
//     → EvaluateConstString resolves cellsTok to "cells" and catches it;
//     this test asserts it does NOT appear so A2b keeps its PASS status.
//
// These negative tests are required by ai-robust §"工具选定后强制盲区自检".
// The tests are vacuously true today (production AST has no such forms), which
// confirms that upgrading A2a/A2b from BasicLit to EvaluateConstString does not
// introduce false positives.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A2_ConstEvalBypassBlindSpots(t *testing.T) {
	// Blind-spot BS-A: const Ident as strings.HasPrefix second arg evaluating to
	// a banned prefix. EvaluateConstString resolves it; a production occurrence
	// would mean A2a already catches it and a developer should not be able to
	// bypass the funnel this way.
	hasPrefixConstDiags := RunTyped(t, TypedOpts{Tests: false},
		[]string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
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
					// Only flag non-BasicLit second args that const-eval to a banned prefix.
					// BasicLit forms are already caught by A2a; this catches the bypass
					// where an Ident (cross-package const) is used instead.
					if _, isLit := call.Args[1].(*ast.BasicLit); isLit {
						return // already covered by A2a
					}
					lit, ok := EvaluateConstString(p.TypesInfo, call.Args[1])
					if !ok {
						return
					}
					for _, banned := range locatorLayoutPrefixes {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(call.Pos()).Line,
								Message: "A2-BS-A (const-eval bypass): strings.HasPrefix(_, <const>=" +
									strconv.Quote(lit) + ") outside Locator funnel — " +
									"A2a now catches this; presence indicates a regression in bypass coverage",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A2.BLINDSPOT.CONST-HASPFIX", hasPrefixConstDiags)

	// Blind-spot BS-B: const Ident as equality operand evaluating to a banned token.
	equalityConstDiags := RunTyped(t, TypedOpts{Tests: false},
		[]string{"./kernel/metadata/...", "./kernel/governance/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
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
					// Skip when either operand is a BasicLit — that form is
					// already covered by A2b; this blind-spot catches Ident
					// operands (cross-package const refs) specifically.
					if _, isLit := bin.X.(*ast.BasicLit); isLit {
						return
					}
					if _, isLit := bin.Y.(*ast.BasicLit); isLit {
						return
					}
					var lit string
					var ok bool
					lit, ok = EvaluateConstString(p.TypesInfo, bin.Y)
					if !ok {
						lit, ok = EvaluateConstString(p.TypesInfo, bin.X)
					}
					if !ok {
						return
					}
					for _, banned := range locatorLayoutTokens {
						if lit == banned {
							d = append(d, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(bin.Pos()).Line,
								Message: "A2-BS-B (const-eval bypass): <expr> ==/!= <const>=" +
									strconv.Quote(lit) + " outside Locator funnel — " +
									"A2b now catches this; presence indicates a regression in bypass coverage",
							})
							break
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A2.BLINDSPOT.CONST-EQ", equalityConstDiags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A5_ConsumerPathFunnel enforces that
// filepath.Join callsites in the consumer packages (kernel/governance,
// cmd/gocell, kernel/metadata outside Locator funnel files) do not reconstruct
// conventional layout paths by passing literal "cells" or "cmd" as arguments.
//
// Consumer code must derive paths from Locator output (CellMeta.File,
// SliceMeta.File, AssemblyMeta.File, etc.) rather than hardcoding the
// conventional layout topology.
//
// AI-robust grading:
//   - Upstream: Medium (archtest caller allowlist; Go type system cannot
//     prevent package-internal code from calling filepath.Join freely).
//     Upstream Hard upgrade path tracked by gh issue #1235.
//   - Downstream: Hard (form-uniqueness: EvaluateConstString resolves every
//     filepath.Join argument; any arg that evaluates to a banned token fails).
//
// Blind spots enforced by TestLOCATOR_DISCOVERY_FUNNEL_01_A5_BlindSpots.
//
// Two files are permanently allowlisted via locatorA5AllowedFiles:
//   - cmd/gocell/app/scaffold.go: write path (layout generator); does not
//     consume discovery paths, produces them.
//   - kernel/metadata/assembly_derive.go: "cmd" is the conventional output
//     directory name for the cmd/<id>/main.go entrypoint, guarded by
//     isConventionalAssemblyPath so Manifest-mode assemblies use
//     path.Dir(asm.File)/main.go instead.
func TestLOCATOR_DISCOVERY_FUNNEL_01_A5_ConsumerPathFunnel(t *testing.T) {
	diags := RunTyped(t, TypedOpts{Tests: false}, locatorConsumerScanPatterns(),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				// Skip test files.
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				// Skip Locator funnel files (they legitimately hold layout tokens).
				if locatorIsAllowedFile(rel) {
					continue
				}
				// Skip A5-specific allowlisted files (write paths / layout
				// generators / conventional-token sites with explicit guards).
				if locatorA5AllowedFiles[filepath.ToSlash(rel)] {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !locatorIsFilepathJoin(call) || len(call.Args) == 0 {
						return
					}
					for _, arg := range call.Args {
						lit, ok := EvaluateConstString(p.TypesInfo, arg)
						if !ok {
							continue
						}
						for _, banned := range locatorConsumerPathTokens {
							if lit == banned {
								d = append(d, Diagnostic{
									Rel:  rel,
									Line: p.Fset.Position(call.Pos()).Line,
									Message: "A5 (Hard downstream): filepath.Join arg " +
										strconv.Quote(lit) +
										" reconstructs a conventional layout path; " +
										"use Locator output (e.g. CellMeta.File) instead",
								})
								return // one diagnostic per callsite
							}
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A5", diags)
}

// TestLOCATOR_DISCOVERY_FUNNEL_01_A5_BlindSpots asserts that the two A5 blind
// spots are absent from the consumer-path scan scope:
//
//  1. path.Join (not filepath.Join) with a banned token — different callee, same
//     semantic violation.
//
//  2. String concatenation that rebuilds a banned token ("ce"+"lls") — the
//     EvaluateConstString constant folding via go/types handles BinaryExpr ADD,
//     but hand-split literals are unusual in production; this negative test
//     ensures they never appear.
//
// Both are required reverse negative tests per ai-robust §"工具选定后强制盲区自检".
func TestLOCATOR_DISCOVERY_FUNNEL_01_A5_BlindSpots(t *testing.T) {
	// Blind-spot A5-BS1: path.Join (not filepath.Join) with banned token.
	pathJoinDiags := RunTyped(t, TypedOpts{Tests: false}, locatorConsumerScanPatterns(),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if locatorIsAllowedFile(rel) {
					continue
				}
				if locatorA5AllowedFiles[filepath.ToSlash(rel)] {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel == nil || sel.Sel.Name != "Join" {
						return
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "path" {
						return
					}
					for _, arg := range call.Args {
						lit, ok := EvaluateConstString(p.TypesInfo, arg)
						if !ok {
							continue
						}
						for _, banned := range locatorConsumerPathTokens {
							if lit == banned {
								d = append(d, Diagnostic{
									Rel:  rel,
									Line: p.Fset.Position(call.Pos()).Line,
									Message: "A5-BS1 (path.Join blind spot): path.Join arg " +
										strconv.Quote(lit) +
										" reconstructs a conventional layout path — A5 only scans filepath.Join",
								})
								return
							}
						}
					}
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A5.BLINDSPOT.PATHJOIN", pathJoinDiags)

	// Blind-spot A5-BS2: string concatenation rebuilding a banned token in
	// filepath.Join args. EvaluateConstString handles BinaryExpr ADD via
	// go/types constant folding, so "ce"+"lls" would be resolved to "cells"
	// and caught by A5. This negative test confirms no such form exists.
	concatDiags := RunTyped(t, TypedOpts{Tests: false}, locatorConsumerScanPatterns(),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if locatorIsAllowedFile(rel) {
					continue
				}
				if locatorA5AllowedFiles[filepath.ToSlash(rel)] {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if !locatorIsFilepathJoin(call) {
						return
					}
					EachInChildren[ast.BinaryExpr](call, func(bin *ast.BinaryExpr) {
						if bin.Op != token.ADD {
							return
						}
						// Only flag if both operands are basic literals (split literal concat).
						_, lhsIsLit := bin.X.(*ast.BasicLit)
						_, rhsIsLit := bin.Y.(*ast.BasicLit)
						if !lhsIsLit || !rhsIsLit {
							return
						}
						lit, ok := EvaluateConstString(p.TypesInfo, bin)
						if !ok {
							return
						}
						for _, banned := range locatorConsumerPathTokens {
							if lit == banned {
								d = append(d, Diagnostic{
									Rel:  rel,
									Line: p.Fset.Position(call.Pos()).Line,
									Message: "A5-BS2 (concat blind spot): filepath.Join arg " +
										strconv.Quote(lit) +
										" from literal concat — present despite A5 catching it via const-eval",
								})
								return
							}
						}
					})
				})
			}
			return d
		})
	Report(t, "LOCATOR-DISCOVERY-FUNNEL-01.A5.BLINDSPOT.CONCAT", concatDiags)
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

// locatorIsFilepathJoin reports whether the call expression is filepath.Join(...).
func locatorIsFilepathJoin(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "filepath" && sel.Sel.Name == "Join"
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
