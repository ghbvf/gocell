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
//     "no constraint").
//
// Multiple directives in the header are AND-combined, matching the merge
// semantics in go/build/read.go::readGoInfo (legacy // +build lines are
// joined with modern //go:build via logical AND).
//
// This helper replaces three independent bufio.Scanner+constraint.Parse
// duplicates that existed in build_constraint_test.go,
// ci_integration_discovery_invariants_test.go, and the (now removed)
// extractBuildTags in buildtags_test.go.
//
// ref: golang/go src/go/build/constraint/expr.go
// ref: golang/go src/go/build/read.go::readGoInfo
func ParseBuildConstraint(filePath string) (constraint.Expr, error) {
	fset := token.NewFileSet()
	// PackageClauseOnly keeps the parser cheap: it stops after `package …`,
	// but ParseComments still gathers leading comments — that's exactly the
	// region that may carry build constraints.
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		return nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, err)
	}

	var combined constraint.Expr
	for _, cg := range file.Comments {
		// Only CommentGroups that end strictly before the package clause are
		// in the constraint zone (Go spec: build constraints precede the
		// package clause and are separated from it by a blank line — the
		// parser's CommentGroup boundary already enforces the blank-line
		// rule because a blank line splits CommentGroups).
		if cg.End() >= file.Package {
			break
		}
		var mergeErr error
		combined, mergeErr = mergeGroupDirectives(filePath, cg.List, combined)
		if mergeErr != nil {
			return nil, mergeErr
		}
	}
	return combined, nil
}

// mergeGroupDirectives scans one CommentGroup's comment lines for build
// directives and AND-merges them into combined. Extracted to reduce the
// cognitive complexity of ParseBuildConstraint.
func mergeGroupDirectives(filePath string, comments []*ast.Comment, combined constraint.Expr) (constraint.Expr, error) {
	for _, c := range comments {
		line := c.Text
		if !constraint.IsGoBuild(line) && !constraint.IsPlusBuild(line) {
			continue
		}
		expr, perr := constraint.Parse(line)
		if perr != nil {
			return nil, fmt.Errorf("typeseval.ParseBuildConstraint(%s): %w", filePath, perr)
		}
		if combined == nil {
			combined = expr
			continue
		}
		combined = &constraint.AndExpr{X: combined, Y: expr}
	}
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
