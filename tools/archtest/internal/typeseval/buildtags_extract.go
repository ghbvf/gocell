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
//     lines are ignored (legacy syntax retained for old-toolchain compat).
//  2. If only // +build lines are present (no //go:build), they are
//     AND-merged into a single expression per go/build/constraint package doc.
//
// This helper replaces three independent bufio.Scanner+constraint.Parse
// duplicates that existed in build_constraint_test.go,
// ci_integration_discovery_invariants_test.go, and the (now removed)
// extractBuildTags in buildtags_test.go.
//
// ref: golang/go src/go/build/constraint/expr.go
// ref: golang/go src/go/build/build.go::parseFileHeader (lines 1627-1662)
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
	// plusBuildExprs accumulates // +build lines, AND-merged when no //go:build is present.
	var goBuildExpr constraint.Expr
	var plusBuildExprs []constraint.Expr

	for _, cg := range file.Comments {
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
		var mergeErr error
		goBuildExpr, plusBuildExprs, mergeErr = collectGroupDirectives(filePath, cg.List, goBuildExpr, plusBuildExprs)
		if mergeErr != nil {
			return nil, mergeErr
		}
	}

	return finalizeConstraint(goBuildExpr, plusBuildExprs), nil
}

// collectGroupDirectives scans one CommentGroup's comment lines for build
// directives and collects them into the running goBuildExpr / plusBuildExprs
// accumulators. Returns an error if a second //go:build line is encountered
// (errMultipleGoBuild semantics from go/build/build.go:1660).
// Extracted to reduce the cognitive complexity of ParseBuildConstraint.
func collectGroupDirectives(
	filePath string,
	comments []*ast.Comment,
	goBuildExpr constraint.Expr,
	plusBuildExprs []constraint.Expr,
) (constraint.Expr, []constraint.Expr, error) {
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
			expr, perr := constraint.Parse(line)
			if perr != nil {
				return nil, nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, perr)
			}
			plusBuildExprs = append(plusBuildExprs, expr)
		}
	}
	return goBuildExpr, plusBuildExprs, nil
}

// finalizeConstraint resolves the collected directives into a single
// constraint.Expr following cmd/go precedence rules:
//   - //go:build is authoritative when present; // +build lines are ignored.
//   - When only // +build lines are present, AND-merge them.
//   - Returns nil when neither kind was found.
func finalizeConstraint(goBuildExpr constraint.Expr, plusBuildExprs []constraint.Expr) constraint.Expr {
	if goBuildExpr != nil {
		return goBuildExpr
	}
	if len(plusBuildExprs) == 0 {
		return nil
	}
	combined := plusBuildExprs[0]
	for _, e := range plusBuildExprs[1:] {
		combined = &constraint.AndExpr{X: combined, Y: e}
	}
	return combined
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
