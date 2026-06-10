//go:build archtest

// INVARIANT: PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01
//
// PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01 — the projection_checkpoints
// owner column is reserved-but-unwritten in v1.
//
// Migration 045 provisions projection_checkpoints.owner (TEXT NOT NULL, default
// empty string) for a future v1.1 multi-pod pessimistic claim (ADR §Q5, ref Axon
// token_entry.owner). v1 is single-pod and MUST NOT write that column: silently
// writing a stale/empty owner would seed dirty claim state that v1.1 then has to
// reconcile. This rule scans Go string-literal SQL in adapters/postgres and fires
// on any INSERT INTO / UPDATE statement that targets projection_checkpoints and
// references the owner column. The PG adapter's upsert deliberately omits owner
// from both the column list and the SET clause.
//
// When v1.1 enables pessimistic claim, the owner write becomes legitimate; this
// rule (and its fixture) is deleted in the SAME PR that introduces the claim
// write path — it is a v1-scoped guard, not a permanent invariant.
//
// # AI-robust grading
//
//   - Medium (AST BasicLit.Value structured match: extract every Go string
//     literal, identify INSERT/UPDATE-into-projection_checkpoints, reject the
//     owner token). It is NOT Hard: a SQL column reference inside a string
//     literal cannot be made type-unexpressible in Go — the column name is opaque
//     text. The grade matches the sibling SQL-content scans
//     MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01 (Medium) and DEAD-CODE-01 downstream
//     (Medium, AST BasicLit.Value exact-match). The pre-grade is fixed by the ADR
//     ("input-struct field exclusion (SQL-write variant)").
//   - Negative coverage: TestProjectionCheckpointOwnerColumnV1Reserved01_RedFixture
//     runs the SAME detector over a synthetic fixture whose write literal references
//     owner, asserting the detector fires (and does NOT fire on the fixture's benign
//     SELECT) — a detector regression fails CI rather than degrading to a silent
//     vacuous-green.
//
// # Blind spots (forms the BasicLit scan cannot see)
//
//   - B1. Dynamic SQL construction: an owner write assembled at runtime rather
//     than as one static literal would escape the main BasicLit scan. Reverse
//     check ..._ReverseBlindSpot_NoDynamicSQL asserts no production code in
//     adapters/postgres builds projection_checkpoints SQL via any AST-recognizable
//     dynamic form — string `+` concatenation, fmt.Sprintf / fmt.Fprintf,
//     strings.Join, or *.WriteString (strings.Builder / bytes.Buffer). Residual:
//     a table name sourced from a non-literal variable (e.g. a const threaded
//     through %s) is unreachable by an AST-only scan; v1 uses static const SQL
//     only, and PG-REPO-AMBIENT-TX-01 + code review cover that tail.
//
//   - B2. Reads of owner (SELECT … owner FROM projection_checkpoints): out of
//     scope by design. Reading the reserved column yields the harmless empty
//     default; only WRITES seed dirty claim state. The detector is INSERT/UPDATE-
//     scoped and the fixture's readSQL anchors that the read is intentionally not
//     flagged.
//
//   - B3. The migration .sql CREATE TABLE legitimately declares owner. It is a
//     SQL file, not Go source, so the Go-AST BasicLit scan never sees it —
//     automatically out of scope.
//
// ref: adapters/postgres/projection_checkpoint_store.go (upsert omits owner)
// ref: adapters/postgres/migrations/045_create_projection_checkpoints.sql (owner reserved)
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §Q5
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const projectionCheckpointsTable = "projection_checkpoints"

// ownerTokenRE matches the bare owner column token (word-boundary, case-
// insensitive). `_` is a word char, so it does not match inside composite
// identifiers like an unrelated `something_owner` column (none exist on this
// table; the only columns are cell_id/projection_id/offset_seq/owner/updated_at).
var ownerTokenRE = regexp.MustCompile(`(?i)\bowner\b`)

// ownerColumnWriteDiags is the shared detector: for every Go string literal that
// is an INSERT/UPDATE targeting projection_checkpoints and references owner, emit
// one diagnostic. Shared by the production rule and the RED fixture test so the
// negative case exercises the real detection path, not a re-implementation.
func ownerColumnWriteDiags(p *Pass) []Diagnostic {
	var diags []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
			val, ok := StringLitValue(lit)
			if !ok {
				return
			}
			if !isProjectionCheckpointWriteSQL(val) {
				return
			}
			if !ownerTokenRE.MatchString(val) {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(lit.Pos()).Line,
				Message: "INSERT/UPDATE on projection_checkpoints references the reserved owner " +
					"column; v1 must not write owner (ADR §Q5; reading it is harmless/unguarded). Remove owner from the " +
					"write path — it is provisioned for v1.1 pessimistic claim only.",
			})
		})
	}
	return diags
}

// isProjectionCheckpointWriteSQL reports whether sql is an INSERT/UPDATE write
// statement targeting projection_checkpoints. "UPDATE " with a trailing space
// matches both top-level UPDATE and the ON CONFLICT "DO UPDATE SET" clause while
// avoiding the column name "updated_at".
func isProjectionCheckpointWriteSQL(sql string) bool {
	up := strings.ToUpper(sql)
	if !strings.Contains(up, strings.ToUpper(projectionCheckpointsTable)) {
		return false
	}
	return strings.Contains(up, "INSERT INTO") || strings.Contains(up, "UPDATE ")
}

// TestProjectionCheckpointOwnerColumnV1Reserved01 is the forward (production)
// assertion: no INSERT/UPDATE on projection_checkpoints in adapters/postgres may
// reference the reserved owner column.
func TestProjectionCheckpointOwnerColumnV1Reserved01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	diags := Run(t, AST(DirsScope(root, []string{"adapters/postgres"})), ownerColumnWriteDiags)
	Report(t, "PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01", diags)
}

// TestProjectionCheckpointOwnerColumnV1Reserved01_RedFixture runs the REAL
// detector over the synthetic fixture (internal/projectioncheckpointownerfixture)
// instead of asserting on hand-built inputs. The fixture declares badUpsertSQL
// (owner in an INSERT/UPDATE — must fire) and readSQL (owner in a SELECT — must
// NOT fire). A detector regression fails this test.
func TestProjectionCheckpointOwnerColumnV1Reserved01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}
	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/projectioncheckpointownerfixture/..."}),

		ownerColumnWriteDiags)

	require.Len(t, diags, 1,
		"RED fixture: detector must fire exactly once — on badUpsertSQL (INSERT/UPDATE owner) "+
			"and NOT on readSQL (SELECT owner). got: %v", diags)
	assert.Contains(t, diags[0].Message, "owner")
}

// TestProjectionCheckpointOwnerColumnV1Reserved01_ReverseBlindSpot_NoDynamicSQL
// (B1) asserts no production string-concatenation expression in adapters/postgres
// mentions projection_checkpoints. The table SQL must stay a static string
// literal the BasicLit scan can see; a dynamically concatenated write could
// smuggle the owner column past this rule.
func TestProjectionCheckpointOwnerColumnV1Reserved01_ReverseBlindSpot_NoDynamicSQL(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	const msg = "B1 blind spot: projection_checkpoints SQL assembled dynamically " +
		"(`+` concatenation / fmt.Sprintf|Fprintf / strings.Join / *.WriteString); " +
		"keep it a single static literal so the owner-column scan stays effective"
	diags := Run(t, AST(DirsScope(root, []string{"adapters/postgres"})), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)

			EachInSubtree[ast.BinaryExpr](f, func(be *ast.BinaryExpr) {
				if be.Op == token.ADD && nodeMentionsCheckpointTable(be) {
					out = append(out, Diagnostic{Rel: rel, Line: p.Fset.Position(be.Pos()).Line, Message: msg})
				}
			})

			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if isDynamicSQLBuilderCall(call) && callArgsMentionCheckpointTable(call) {
					out = append(out, Diagnostic{Rel: rel, Line: p.Fset.Position(call.Pos()).Line, Message: msg})
				}
			})
		}
		return out
	})

	assert.Empty(t, diags,
		"B1 reverse: projection_checkpoints SQL must be a single static string literal, "+
			"not built via dynamic construction")
}

// nodeMentionsCheckpointTable reports whether any string literal in n's subtree
// contains the projection_checkpoints table name.
func nodeMentionsCheckpointTable(n ast.Node) bool {
	found := false
	EachInSubtree[ast.BasicLit](n, func(lit *ast.BasicLit) {
		if v, ok := StringLitValue(lit); ok &&
			strings.Contains(strings.ToUpper(v), strings.ToUpper(projectionCheckpointsTable)) {
			found = true
		}
	})
	return found
}

// isDynamicSQLBuilderCall reports whether call is one of the AST-recognizable
// dynamic string-builder forms: fmt.Sprintf / fmt.Fprintf / strings.Join, or any
// method named WriteString (strings.Builder / bytes.Buffer). Matched by selector
// name only (no type resolution) — a deliberately broad net for the reverse check.
func isDynamicSQLBuilderCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "WriteString":
		return true
	case "Sprintf", "Fprintf":
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == "fmt"
	case "Join":
		x, ok := sel.X.(*ast.Ident)
		return ok && x.Name == "strings"
	}
	return false
}

// callArgsMentionCheckpointTable reports whether any argument subtree of call
// contains a string literal naming the projection_checkpoints table.
func callArgsMentionCheckpointTable(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if nodeMentionsCheckpointTable(arg) {
			return true
		}
	}
	return false
}

// TestProjectionCheckpointOwnerColumnV1Reserved01_DynamicSQLDetectorFires proves
// the B1 reverse-check detector is NOT vacuous: it must fire on every dynamic
// construction form the godoc claims to cover (fmt.Sprintf, strings.Join,
// *.WriteString, and `+`). Without this, the reverse self-check would pass
// trivially because production happens to contain zero dynamic forms — a
// detector regression (e.g. a typo in isDynamicSQLBuilderCall) would go unnoticed.
func TestProjectionCheckpointOwnerColumnV1Reserved01_DynamicSQLDetectorFires(t *testing.T) {
	t.Parallel()
	const src = `package x

import (
	"fmt"
	"strings"
)

func a() string { return fmt.Sprintf("INSERT INTO projection_checkpoints (owner) VALUES (%s)", "v") }
func b() string {
	return strings.Join([]string{"UPDATE projection_checkpoints SET owner =", "v"}, " ")
}
func c(sb *strings.Builder) { sb.WriteString("INSERT INTO projection_checkpoints (owner) VALUES ('')") }
func d() string            { return "INSERT INTO projection_checkpoints (" + "owner)" }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	require.NoError(t, err)

	var dynamicCalls, concatExprs int
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if isDynamicSQLBuilderCall(call) && callArgsMentionCheckpointTable(call) {
			dynamicCalls++
		}
	})
	EachInSubtree[ast.BinaryExpr](file, func(be *ast.BinaryExpr) {
		if be.Op == token.ADD && nodeMentionsCheckpointTable(be) {
			concatExprs++
		}
	})

	assert.Equal(t, 3, dynamicCalls,
		"detector must fire on fmt.Sprintf + strings.Join + *.WriteString building projection_checkpoints SQL")
	assert.GreaterOrEqual(t, concatExprs, 1,
		"detector must fire on `+` concatenation building projection_checkpoints SQL")
}
