package archtest

// invariants:
//   - INVARIANT: STORAGE-BACKEND-PG-WIRING-01
//   - INVARIANT: STORAGE-BACKEND-MEMORY-NO-PG-01
//
// storage_backend_test.go enforces two rules about how PG adapters are wired in
// cmd/corebundle cell modules:
//
//   - STORAGE-BACKEND-PG-WIRING-01: access_module.go and audit_module.go each
//     must (a) import "adapters/postgres", (b) call adapterpg.NewOutboxWriter()
//     AND adapterpg.NewTxManager(...), and (c) those calls must appear inside an
//     if-block whose condition tests StorageBackend == "postgres".
//
//   - STORAGE-BACKEND-MEMORY-NO-PG-01: access_module.go and audit_module.go must
//     NOT contain any unconditional top-level call to adapterpg.NewPool, ensuring
//     the PG pool is created only in config_module.go's postgres branch.
//
// Both rules scan the concrete source files using go/ast so that structural
// changes (renaming locals, reordering statements) are caught even when the
// import is indirect or aliased.
//
// Variable propagation contract:
//
//	conditionMentionsPostgres supports SelectorExpr literals AND local variable
//	assignments within the same function body. Specifically:
//	  - `backend := shared.Topology.StorageBackend; if backend == "postgres"`
//	  - `backend := "postgres"; if backend == "postgres"`
//	Both forms are detected by buildLocalVarValues which collects same-function
//	:=/= assignments before the condition check runs.
//	Cross-function calls and method-chain expressions (e.g. getBackend() == "postgres")
//	are deliberately out of scope. File an upgrade if a refactor introduces these patterns.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	ruleStorageBackendPGWiring01   = "STORAGE-BACKEND-PG-WIRING-01"
	ruleStorageBackendMemoryNoPG01 = "STORAGE-BACKEND-MEMORY-NO-PG-01"
	pgStorageBackendCondValue      = "postgres"
	pgNewOutboxWriterIdent         = "NewOutboxWriter"
	pgNewTxManagerIdent            = "NewTxManager"
	pgNewPoolIdent                 = "NewPool"
)

// TestStorageBackendPGWiring01 verifies that access_module.go and audit_module.go
// each wire adapterpg.NewOutboxWriter + adapterpg.NewTxManager inside an
// if-block that checks StorageBackend == "postgres".
//
// Rationale: kernel/cell.ResolveEmitter requires both OutboxWriter and TxRunner
// non-nil for DurabilityDurable cells. Without this wiring, postgres-topology
// deployments fail at startup with ERR_CELL_MISSING_OUTBOX.
func TestStorageBackendPGWiring01(t *testing.T) {
	root := findModuleRoot(t)
	targets := []string{
		filepath.Join(root, "cmd", "corebundle", "access_module.go"),
		filepath.Join(root, "cmd", "corebundle", "audit_module.go"),
	}

	for _, target := range targets {
		rel, _ := filepath.Rel(root, target)
		rel = filepath.ToSlash(rel)
		t.Run(rel, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
			require.NoErrorf(t, err, "%s: parse failed", rel)

			info := analyzeStorageBackendFile(file)

			assert.True(t, info.importsPGAdapter,
				"%s: %s — file must import the adapters/postgres package (alias typically adapterpg)",
				rel, ruleStorageBackendPGWiring01)

			assert.True(t, info.hasNewOutboxWriterInPGBranch,
				"%s: %s — file must call adapterpg.NewOutboxWriter() inside a "+
					`if StorageBackend == "postgres" block`,
				rel, ruleStorageBackendPGWiring01)

			assert.True(t, info.hasNewTxManagerInPGBranch,
				"%s: %s — file must call adapterpg.NewTxManager(...) inside a "+
					`if StorageBackend == "postgres" block`,
				rel, ruleStorageBackendPGWiring01)
		})
	}
}

// TestStorageBackendMemoryNoPG01 verifies that access_module.go and
// audit_module.go do NOT contain an unconditional call to adapterpg.NewPool.
// Pool creation must remain in config_module.go's postgres branch only.
//
// An unconditional NewPool in access/audit modules would attempt a PG
// connection even when running in memory mode, causing startup failures
// in environments without a Postgres instance.
func TestStorageBackendMemoryNoPG01(t *testing.T) {
	root := findModuleRoot(t)
	targets := []string{
		filepath.Join(root, "cmd", "corebundle", "access_module.go"),
		filepath.Join(root, "cmd", "corebundle", "audit_module.go"),
	}

	for _, target := range targets {
		rel, _ := filepath.Rel(root, target)
		rel = filepath.ToSlash(rel)
		t.Run(rel, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
			require.NoErrorf(t, err, "%s: parse failed", rel)

			info := analyzeStorageBackendFile(file)

			assert.False(t, info.hasUnconditionalNewPool,
				"%s: %s — file must not contain an unconditional call to adapterpg.NewPool; "+
					"pool creation belongs exclusively in config_module.go's postgres branch",
				rel, ruleStorageBackendMemoryNoPG01)
		})
	}
}

// storageBackendFileInfo holds the analysis results for a single module file.
type storageBackendFileInfo struct {
	// importsPGAdapter is true when the file imports "adapters/postgres".
	importsPGAdapter bool
	// hasNewOutboxWriterInPGBranch is true when the file contains a call to
	// <pgAlias>.NewOutboxWriter() inside an if-block that checks
	// StorageBackend == "postgres".
	hasNewOutboxWriterInPGBranch bool
	// hasNewTxManagerInPGBranch is true when the file contains a call to
	// <pgAlias>.NewTxManager(...) inside an if-block that checks
	// StorageBackend == "postgres".
	hasNewTxManagerInPGBranch bool
	// hasUnconditionalNewPool is true when the file contains a call to
	// <pgAlias>.NewPool(...) outside of any if-block.
	hasUnconditionalNewPool bool
}

// analyzeStorageBackendFile inspects a parsed file for the storage-backend
// wiring rules. It is a pure AST function — no file I/O.
//
// Variable propagation: for each top-level function declaration the analyzer
// builds a per-function localVarValues map (via buildLocalVarValues) before
// evaluating if-conditions, so assignments of the form
//
//	backend := shared.Topology.StorageBackend
//	backend := "postgres"
//
// within the same function body are tracked and resolved in conditionMentionsPostgres.
func analyzeStorageBackendFile(file *ast.File) storageBackendFileInfo {
	info := storageBackendFileInfo{}

	// Collect the local alias(es) for "adapters/postgres" imports.
	pgAliases := collectPGAdapterAliases(file)
	info.importsPGAdapter = len(pgAliases) > 0

	if !info.importsPGAdapter {
		return info
	}

	// Walk function declarations so we can build per-function localVarValues.
	// Strategy: for each FuncDecl, collect variable assignments from the body,
	// then scan IfStmts within that function.
	scanner.EachInSubtree[ast.FuncDecl](file, func(funcDecl *ast.FuncDecl) {
		if funcDecl.Body == nil {
			return
		}
		localVars := buildLocalVarValues(funcDecl.Body)

		scanner.EachInSubtree[ast.IfStmt](funcDecl.Body, func(ifStmt *ast.IfStmt) {
			isPGBranch := conditionMentionsPostgres(ifStmt.Cond, localVars)
			// Scan the if-body for the required calls.
			scanner.EachInSubtree[ast.CallExpr](ifStmt.Body, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				if !pgAliases[pkgIdent.Name] {
					return
				}
				if isPGBranch {
					switch sel.Sel.Name {
					case pgNewOutboxWriterIdent:
						info.hasNewOutboxWriterInPGBranch = true
					case pgNewTxManagerIdent:
						info.hasNewTxManagerInPGBranch = true
					}
				}
			})
		})
	})

	// Detect unconditional NewPool calls: any call to <pgAlias>.NewPool that
	// is NOT inside an if-block whose condition mentions "postgres".
	info.hasUnconditionalNewPool = hasUnconditionalPGCall(file, pgAliases, pgNewPoolIdent)

	return info
}

// buildLocalVarValues walks a function body and returns a map of local variable
// name to its string value for assignments where the RHS is either:
//   - a selector ending in ".StorageBackend" (e.g. shared.Topology.StorageBackend),
//     recorded as the sentinel storageBackendSentinel
//   - a string literal (e.g. backend := "postgres")
//
// Only :=/= simple assignments are tracked. Compound assignments, function
// calls, and method-chain expressions are not tracked (out of scope).
//
// The returned map is used by conditionMentionsPostgres to resolve identifier
// references within the same function body.
const storageBackendSentinel = "\x00storageBackend"

func buildLocalVarValues(body *ast.BlockStmt) map[string]string {
	result := map[string]string{}
	scanner.EachInSubtree[ast.AssignStmt](body, func(assign *ast.AssignStmt) {
		// Handle := and = assignments with equal number of LHS/RHS.
		if len(assign.Lhs) != len(assign.Rhs) {
			return
		}
		for i := range assign.Lhs {
			ident, ok := assign.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			switch rhs := assign.Rhs[i].(type) {
			case *ast.SelectorExpr:
				// Matches x.StorageBackend or x.y.StorageBackend (nested SelectorExpr).
				if rhs.Sel.Name == "StorageBackend" {
					result[ident.Name] = storageBackendSentinel
				}
			case *ast.BasicLit:
				if rhs.Kind == token.STRING {
					result[ident.Name] = strings.Trim(rhs.Value, `"`)
				}
			}
		}
	})
	return result
}

// collectPGAdapterAliases returns the set of local import names (aliases) used
// for the "adapters/postgres" package import in the given file.
// A file that imports adapterpg "github.com/ghbvf/gocell/adapters/postgres"
// would return {"adapterpg": true}.
func collectPGAdapterAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasSuffix(path, "/adapters/postgres") {
			continue
		}
		var name string
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
			// Explicit alias: import adapterpg "...adapters/postgres"
			name = imp.Name.Name
		} else {
			// Implicit alias: last path segment.
			parts := strings.Split(path, "/")
			name = parts[len(parts)-1]
		}
		aliases[name] = true
	}
	return aliases
}

// conditionMentionsPostgres returns true when the AST expression `cond` (or
// any boolean sub-expression) compares a string to the literal "postgres".
// Handles:
//   - binary `==` comparisons: `x.StorageBackend == "postgres"`
//   - logical AND/OR chains: `a && b || c`
//   - local variable form: `backend == "postgres"` where backend was assigned
//     from shared.Topology.StorageBackend or from the literal "postgres"
//     within the same function body (resolved via localVarValues)
//
// localVarValues maps local variable names to their resolved string values (or
// the sentinel storageBackendSentinel when assigned from a StorageBackend
// selector). Pass nil or an empty map when no local variable tracking is needed.
//
// Does not handle negated conditions (!=), which is intentional — the rule
// targets the positive branch that performs the wiring.
//
// Cross-function calls (e.g. getBackend() == "postgres") and method-chain
// expressions are deliberately out of scope; use localVarValues for
// same-function variable refactors only.
func conditionMentionsPostgres(cond ast.Expr, localVarValues map[string]string) bool {
	switch e := cond.(type) {
	case *ast.BinaryExpr:
		if e.Op == token.EQL {
			// Direct selector form: x.StorageBackend == "postgres" (or reversed).
			if isSelectorEndingIn(e.X, "StorageBackend") && isStringLiteral(e.Y) {
				return true
			}
			if isSelectorEndingIn(e.Y, "StorageBackend") && isStringLiteral(e.X) {
				return true
			}
			// Variable form: backend == "postgres" where backend was assigned
			// from a StorageBackend selector or from the literal "postgres".
			if resolvedExprMatchesPostgres(e.X, e.Y, localVarValues) {
				return true
			}
			if resolvedExprMatchesPostgres(e.Y, e.X, localVarValues) {
				return true
			}
		}
		// Recurse into AND / OR compound conditions.
		return conditionMentionsPostgres(e.X, localVarValues) || conditionMentionsPostgres(e.Y, localVarValues)
	case *ast.ParenExpr:
		return conditionMentionsPostgres(e.X, localVarValues)
	}
	return false
}

// resolvedExprMatchesPostgres checks if ident is a local variable whose value
// is storageBackendSentinel and peer is the string literal "postgres", OR if
// ident is a local variable with value "postgres" and peer is any expression
// (including the literal "postgres" itself).
//
// This covers two variable forms:
//   - backend := shared.Topology.StorageBackend  →  if backend == "postgres"
//   - backend := "postgres"                      →  if backend == "postgres"
func resolvedExprMatchesPostgres(ident ast.Expr, peer ast.Expr, localVarValues map[string]string) bool {
	if len(localVarValues) == 0 {
		return false
	}
	id, ok := ident.(*ast.Ident)
	if !ok {
		return false
	}
	val, known := localVarValues[id.Name]
	if !known {
		return false
	}
	switch val {
	case storageBackendSentinel:
		// Variable holds StorageBackend; match only when peer is "postgres".
		return isStringLiteral(peer)
	case pgStorageBackendCondValue:
		// Variable holds the literal "postgres"; match when peer is also "postgres"
		// OR when peer is a StorageBackend selector (both sides resolve to postgres).
		return isStringLiteral(peer) || isSelectorEndingIn(peer, "StorageBackend")
	}
	return false
}

// isSelectorEndingIn returns true when expr is a SelectorExpr whose field name
// ends with the given suffix. Used to detect shared.Topology.StorageBackend on
// the RHS of a comparison.
func isSelectorEndingIn(expr ast.Expr, fieldName string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == fieldName
}

// isStringLiteral returns true when expr is a string basic literal equal to pgStorageBackendCondValue.
func isStringLiteral(expr ast.Expr) bool {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	// Strip surrounding quotes.
	return strings.Trim(bl.Value, `"`) == pgStorageBackendCondValue
}

// hasUnconditionalPGCall returns true when the file contains a call to
// <pgAlias>.<funcName>(...) that is NOT nested inside an if-block whose
// condition mentions "postgres". This catches unconditional NewPool calls that
// would run even in memory mode.
//
// Variable propagation: per-function localVarValues (built by buildLocalVarValues)
// is used when evaluating if-conditions so that variable-form guards
// (backend := shared.Topology.StorageBackend; if backend == "postgres")
// are correctly recognized as conditional sites.
func hasUnconditionalPGCall(file *ast.File, pgAliases map[string]bool, funcName string) bool {
	found := false
	// Walk the entire file; when we encounter a CallExpr matching <pgAlias>.<funcName>,
	// verify it is inside a postgres-guarded if-body. EachNode does not expose
	// parent context (no proceed-bool, no stack), so we run two independent
	// scans: one collects all CallExpr sites, the other collects only those
	// inside postgres-guarded IfStmt bodies. Sites in (all - conditional) are
	// unconditional violations.
	type callSite struct{ pos token.Pos }
	var conditionalSites []callSite
	var allSites []callSite

	// Collect all calls to <pgAlias>.<funcName> anywhere in the file.
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if !pgAliases[pkgIdent.Name] {
			return
		}
		if sel.Sel.Name == funcName {
			allSites = append(allSites, callSite{call.Pos()})
		}
	})

	if len(allSites) == 0 {
		return false
	}

	// Collect calls to <pgAlias>.<funcName> that ARE inside a postgres if-body.
	// Iterate per function declaration so localVarValues is function-scoped.
	scanner.EachInSubtree[ast.FuncDecl](file, func(funcDecl *ast.FuncDecl) {
		if funcDecl.Body == nil {
			return
		}
		localVars := buildLocalVarValues(funcDecl.Body)

		scanner.EachInSubtree[ast.IfStmt](funcDecl.Body, func(ifStmt *ast.IfStmt) {
			if !conditionMentionsPostgres(ifStmt.Cond, localVars) {
				return
			}
			scanner.EachInSubtree[ast.CallExpr](ifStmt.Body, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				if pgAliases[pkgIdent.Name] && sel.Sel.Name == funcName {
					conditionalSites = append(conditionalSites, callSite{call.Pos()})
				}
			})
		})
	})

	// An unconditional call exists if any allSites entry is not in conditionalSites.
	conditionalSet := map[token.Pos]bool{}
	for _, cs := range conditionalSites {
		conditionalSet[cs.pos] = true
	}
	for _, cs := range allSites {
		if !conditionalSet[cs.pos] {
			found = true
			break
		}
	}
	return found
}

// ---------------------------------------------------------------------------
// Variable propagation fixtures
// ---------------------------------------------------------------------------

// TestStorageBackendPGWiring01_VariablePropagation verifies that the detector
// correctly handles the refactored variable form:
//
//	backend := shared.Topology.StorageBackend
//	if backend == "postgres" { ... }
//
// This form is semantically equivalent to the direct selector form but was
// previously missed because conditionMentionsPostgres only checked BasicLit
// nodes. buildLocalVarValues now tracks these assignments within the same
// function body.
func TestStorageBackendPGWiring01_VariablePropagation(t *testing.T) {
	// Positive fixture: variable assigned from StorageBackend selector, then
	// compared to "postgres" — detector must recognize the if-block as a PG branch.
	const fixtureVarFormPositive = `package fake
import adapterpg "github.com/ghbvf/gocell/adapters/postgres"
func Provide(shared *struct{ Topology struct{ StorageBackend string } }) {
    backend := shared.Topology.StorageBackend
    if backend == "postgres" {
        _ = adapterpg.NewOutboxWriter()
        _ = adapterpg.NewTxManager(nil)
    }
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", fixtureVarFormPositive, 0)
	require.NoError(t, err, "positive fixture must parse")

	info := analyzeStorageBackendFile(file)

	assert.True(t, info.importsPGAdapter,
		"positive fixture: must detect adapters/postgres import")
	assert.True(t, info.hasNewOutboxWriterInPGBranch,
		"positive fixture: NewOutboxWriter must be detected inside the variable-form PG branch")
	assert.True(t, info.hasNewTxManagerInPGBranch,
		"positive fixture: NewTxManager must be detected inside the variable-form PG branch")
}

// TestStorageBackendPGWiring01_VariablePropagation_NonPostgresLiteral verifies
// that the detector does NOT classify an if-block as a PG branch when the local
// variable holds a non-postgres literal (e.g. "memory").
//
// In practice `if backend == "postgres"` where backend == "memory" is unreachable,
// but the detector works syntactically — it only knows the variable's literal
// value from the assignment, not runtime values.
func TestStorageBackendPGWiring01_VariablePropagation_NonPostgresLiteral(t *testing.T) {
	const fixtureVarFormNonPostgres = `package fake
import adapterpg "github.com/ghbvf/gocell/adapters/postgres"
func Provide(shared *struct{ Topology struct{ StorageBackend string } }) {
    backend := "memory"
    if backend == "postgres" {
        _ = adapterpg.NewOutboxWriter()
        _ = adapterpg.NewTxManager(nil)
    }
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake_nonpg.go", fixtureVarFormNonPostgres, 0)
	require.NoError(t, err, "non-postgres fixture must parse")

	info := analyzeStorageBackendFile(file)

	assert.False(t, info.hasNewOutboxWriterInPGBranch,
		"non-postgres fixture: variable assigned 'memory' must NOT be detected as a PG branch")
	assert.False(t, info.hasNewTxManagerInPGBranch,
		"non-postgres fixture: variable assigned 'memory' must NOT be detected as a PG branch")
}

// TestStorageBackendPGWiring01_VariablePropagation_CallExpression verifies
// that the detector does NOT classify an if-block as a PG branch when the
// condition involves a function call expression on the LHS (e.g. getBackend()).
//
// Cross-function calls are deliberately out of scope for this detector.
// If a refactor introduces `if getBackend() == "postgres"`, file an upgrade
// to extend the tracker with cross-function inlining.
func TestStorageBackendPGWiring01_VariablePropagation_CallExpression(t *testing.T) {
	// Out-of-scope: the condition uses a call expression, not a tracked local variable.
	const fixtureCallExpr = `package fake
import adapterpg "github.com/ghbvf/gocell/adapters/postgres"
func getBackend() string { return "postgres" }
func Provide() {
    if getBackend() == "postgres" {
        _ = adapterpg.NewOutboxWriter()
        _ = adapterpg.NewTxManager(nil)
    }
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake_call.go", fixtureCallExpr, 0)
	require.NoError(t, err, "call-expression fixture must parse")

	info := analyzeStorageBackendFile(file)

	// Call expressions are out of scope: the detector returns false.
	// This is a deliberate limitation documented in the conditionMentionsPostgres godoc.
	assert.False(t, info.hasNewOutboxWriterInPGBranch,
		"call-expr fixture (out-of-scope): getBackend() call must NOT be resolved by the variable tracker")
	assert.False(t, info.hasNewTxManagerInPGBranch,
		"call-expr fixture (out-of-scope): getBackend() call must NOT be resolved by the variable tracker")
}

// TestStorageBackendPGWiring01_VariablePropagation_LiteralPostgresVar verifies
// that a variable explicitly assigned the literal "postgres" is also tracked,
// so `backend := "postgres"; if backend == "postgres"` is recognized.
// This is an edge case — production code should use the StorageBackend selector
// form — but the tracker supports it for robustness.
func TestStorageBackendPGWiring01_VariablePropagation_LiteralPostgresVar(t *testing.T) {
	const fixtureLiteralVar = `package fake
import adapterpg "github.com/ghbvf/gocell/adapters/postgres"
func Provide() {
    backend := "postgres"
    if backend == "postgres" {
        _ = adapterpg.NewOutboxWriter()
        _ = adapterpg.NewTxManager(nil)
    }
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake_literal_var.go", fixtureLiteralVar, 0)
	require.NoError(t, err, "literal-var fixture must parse")

	info := analyzeStorageBackendFile(file)

	assert.True(t, info.hasNewOutboxWriterInPGBranch,
		"literal-var fixture: variable assigned literal 'postgres' must be tracked as PG branch")
	assert.True(t, info.hasNewTxManagerInPGBranch,
		"literal-var fixture: variable assigned literal 'postgres' must be tracked as PG branch")
}
