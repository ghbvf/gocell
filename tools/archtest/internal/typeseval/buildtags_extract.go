package typeseval

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
)

// ParseBuildConstraint extracts the file's build constraint expression using
// go/parser.ParseFile so the CommentGroup boundary, leading-comment position
// rule, and Go-toolchain directive semantics match cmd/go's own reader.
//
// Returns (nil, nil) when the file has no //go:build / // +build directive
// in its header (the comment block that precedes the package clause and is
// separated from it by a blank line — the only zone Go recognizes for build
// constraints). Returns (nil, err) when:
//   - the file cannot be opened or parsed, or
//   - a recognized directive line fails constraint.Parse (fail-closed: a
//     constraint that cannot be evaluated must not be silently treated as
//     "no constraint"), or
//   - the file contains multiple //go:build directives (errMultipleGoBuild
//     per go/build/build.go:1660 — cmd/go rejects such files).
//
// Directive precedence matches cmd/go (go/build/build.go parseFileHeader):
//  1. If a //go:build line is present, it is authoritative; any // +build
//     lines are ignored entirely (legacy syntax retained for old-toolchain
//     compat — cmd/go's shouldBuild only scans +build when goBuild == nil).
//  2. If only // +build lines are present (no //go:build), they are
//     AND-merged into a single expression per go/build/constraint package doc.
//
// Legacy plus-build recognition requires a blank line between the directive's
// CommentGroup and the package clause (cmd/go's parseFileHeader). The
// per-CG blank-line gate is equivalent because AST splits CGs at blank lines.
//
// This helper replaces three independent bufio.Scanner+constraint.Parse
// duplicates that existed in build_constraint_test.go,
// ci_integration_discovery_invariants_test.go, and the (now removed)
// extractBuildTags in buildtags_test.go.
//
// ref: golang/go src/go/build/constraint/expr.go
// ref: golang/go src/go/build/build.go::parseFileHeader (lines 1627-1662)
// ref: golang/go src/go/build/build.go::shouldBuild +build fallback
func ParseBuildConstraint(filePath string) (constraint.Expr, error) {
	fset := token.NewFileSet()
	// PackageClauseOnly keeps the parser cheap: it stops after `package …`,
	// but ParseComments still gathers leading comments — that's exactly the
	// region that may carry build constraints.
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		return nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, err)
	}

	// goBuildExpr holds the single allowed //go:build expression; at most one is permitted.
	// plusBuildLines accumulates raw // +build lines (as strings, not parsed exprs),
	// AND-merged lazily only when no //go:build is present — matching cmd/go's shouldBuild.
	var goBuildExpr constraint.Expr
	var plusBuildLines []string

	for i, cg := range file.Comments {
		// Only CommentGroups that end strictly before the package clause are
		// in the constraint zone (Go spec: build constraints precede the
		// package clause and are separated from it by a blank line — the
		// parser's CommentGroup boundary already enforces the blank-line
		// rule because a blank line splits CommentGroups).
		// Using >= rather than > is conservative: a CommentGroup ending exactly
		// at the package keyword position (no blank-line separation) is
		// non-canonical (gofmt would never produce it) and excluded safely.
		if cg.End() >= file.Package {
			break
		}

		// Per cmd/go parseFileHeader: // +build is only recognized when the
		// directive's CommentGroup is followed by at least one blank line
		// before the next CG (or package clause). Compute the line number of
		// the start of whatever follows this CG.
		var nextLine int
		if i+1 < len(file.Comments) && file.Comments[i+1].End() < file.Package {
			nextLine = fset.Position(file.Comments[i+1].Pos()).Line
		} else {
			nextLine = fset.Position(file.Package).Line
		}
		// Blank line exists iff the next element starts ≥ 2 lines after this
		// CG's last line (one line = comment itself, one line = blank separator).
		plusBuildValid := nextLine-fset.Position(cg.End()).Line >= 2

		var mergeErr error
		goBuildExpr, plusBuildLines, mergeErr = collectGroupDirectives(filePath, cg.List, goBuildExpr, plusBuildLines, plusBuildValid)
		if mergeErr != nil {
			return nil, mergeErr
		}
	}

	return finalizeConstraint(filePath, goBuildExpr, plusBuildLines)
}

// collectGroupDirectives scans one CommentGroup's comment lines for build
// directives and collects them into the running goBuildExpr / plusBuildLines
// accumulators. plusBuildValid controls whether // +build lines in this CG
// are recognized (false when no blank-line gap follows the CG, matching
// cmd/go's parseFileHeader blank-line requirement).
// Returns an error if a second //go:build line is encountered
// (errMultipleGoBuild semantics from go/build/build.go:1660).
// Extracted to reduce the cognitive complexity of ParseBuildConstraint.
func collectGroupDirectives(
	filePath string,
	comments []*ast.Comment,
	goBuildExpr constraint.Expr,
	plusBuildLines []string,
	plusBuildValid bool,
) (constraint.Expr, []string, error) {
	for _, c := range comments {
		line := c.Text
		switch {
		case constraint.IsGoBuild(line):
			if goBuildExpr != nil {
				return nil, nil, fmt.Errorf(
					"typeseval.ParseBuildConstraint(%s): multiple //go:build directives",
					filePath,
				)
			}
			expr, perr := constraint.Parse(line)
			if perr != nil {
				return nil, nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, perr)
			}
			goBuildExpr = expr

		case constraint.IsPlusBuild(line):
			if !plusBuildValid {
				// No blank line between this CG and the next element — cmd/go
				// would not recognize this // +build directive.
				continue
			}
			// Store the raw line; parsing is deferred to finalizeConstraint so
			// that malformed // +build lines are ignored when a valid //go:build
			// is already present (matching cmd/go's shouldBuild fallback logic).
			plusBuildLines = append(plusBuildLines, line)
		}
	}
	return goBuildExpr, plusBuildLines, nil
}

// finalizeConstraint resolves the collected directives into a single
// constraint.Expr following cmd/go precedence rules:
//   - //go:build is authoritative when present; // +build lines are ignored entirely.
//   - When only // +build lines are present, parse and AND-merge them.
//   - Returns nil when neither kind was found.
//
// Parsing // +build lines is deferred here (not in collectGroupDirectives) so
// that malformed legacy lines do not produce errors when a valid //go:build is
// already authoritative — matching cmd/go's shouldBuild which only reaches the
// // +build scanning branch when goBuild == nil.
func finalizeConstraint(filePath string, goBuildExpr constraint.Expr, plusBuildLines []string) (constraint.Expr, error) {
	if goBuildExpr != nil {
		// //go:build is present and authoritative; // +build lines ignored entirely.
		return goBuildExpr, nil
	}
	// No //go:build — parse and AND-merge any // +build lines found.
	var combined constraint.Expr
	for _, line := range plusBuildLines {
		expr, perr := constraint.Parse(line)
		if perr != nil {
			return nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, perr)
		}
		if combined == nil {
			combined = expr
		} else {
			combined = &constraint.AndExpr{X: combined, Y: expr}
		}
	}
	// combined is nil when plusBuildLines is empty — caller treats nil as "no constraint".
	return combined, nil
}

// walkConstraintTags appends every TagExpr's Tag identifier under e to *out,
// recursing through And/Or/Not nodes. Kept unexported because the only
// consumer is the typeseval coverage self-test (TestKnownNonDefaultTags*).
// External callers that need per-tag introspection should use expr.Eval
// directly with their active tag predicate.
func walkConstraintTags(e constraint.Expr, out *[]string) {
	switch v := e.(type) {
	case *constraint.AndExpr:
		walkConstraintTags(v.X, out)
		walkConstraintTags(v.Y, out)
	case *constraint.OrExpr:
		walkConstraintTags(v.X, out)
		walkConstraintTags(v.Y, out)
	case *constraint.NotExpr:
		walkConstraintTags(v.X, out)
	case *constraint.TagExpr:
		*out = append(*out, v.Tag)
	}
}
