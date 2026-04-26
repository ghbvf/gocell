package archtest

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

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		target := target
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
		target := target
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
func analyzeStorageBackendFile(file *ast.File) storageBackendFileInfo {
	info := storageBackendFileInfo{}

	// Collect the local alias(es) for "adapters/postgres" imports.
	pgAliases := collectPGAdapterAliases(file)
	info.importsPGAdapter = len(pgAliases) > 0

	if !info.importsPGAdapter {
		return info
	}

	// Walk the AST to find the relevant call expressions and their context.
	// We track the nesting depth inside "postgres if-blocks" so we know whether
	// a call is conditional or unconditional.
	//
	// Strategy: walk the whole file body; whenever we enter an ast.IfStmt whose
	// condition (or its sub-conditions) contain a string comparison with
	// "postgres", we mark the body as a "postgres branch". Calls inside that body
	// are recorded as conditional.

	ast.Inspect(file, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		isPGBranch := conditionMentionsPostgres(ifStmt.Cond)
		// Scan the if-body for the required calls.
		ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if !pgAliases[pkgIdent.Name] {
				return true
			}
			if isPGBranch {
				switch sel.Sel.Name {
				case pgNewOutboxWriterIdent:
					info.hasNewOutboxWriterInPGBranch = true
				case pgNewTxManagerIdent:
					info.hasNewTxManagerInPGBranch = true
				}
			}
			return true
		})
		return true
	})

	// Detect unconditional NewPool calls: any call to <pgAlias>.NewPool that
	// is NOT inside an if-block whose condition mentions "postgres".
	info.hasUnconditionalNewPool = hasUnconditionalPGCall(file, pgAliases, pgNewPoolIdent)

	return info
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
//
// Does not handle negated conditions (!=), which is intentional — the rule
// targets the positive branch that performs the wiring.
func conditionMentionsPostgres(cond ast.Expr) bool {
	switch e := cond.(type) {
	case *ast.BinaryExpr:
		if e.Op == token.EQL {
			if isStringLiteral(e.X, pgStorageBackendCondValue) || isStringLiteral(e.Y, pgStorageBackendCondValue) {
				return true
			}
		}
		// Recurse into AND / OR compound conditions.
		return conditionMentionsPostgres(e.X) || conditionMentionsPostgres(e.Y)
	case *ast.ParenExpr:
		return conditionMentionsPostgres(e.X)
	}
	return false
}

// isStringLiteral returns true when expr is a string basic literal equal to want.
func isStringLiteral(expr ast.Expr, want string) bool {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	// Strip surrounding quotes.
	return strings.Trim(bl.Value, `"`) == want
}

// hasUnconditionalPGCall returns true when the file contains a call to
// <pgAlias>.<funcName>(...) that is NOT nested inside an if-block whose
// condition mentions "postgres". This catches unconditional NewPool calls that
// would run even in memory mode.
func hasUnconditionalPGCall(file *ast.File, pgAliases map[string]bool, funcName string) bool {
	found := false
	// Walk the entire file; when we encounter a CallExpr matching <pgAlias>.<funcName>,
	// verify it is inside a postgres-guarded if-body by scanning the ancestor chain.
	// ast.Inspect does not expose parent nodes, so instead we walk all IfStmt bodies
	// that ARE postgres-guarded and collect the call targets within them, then negate.
	type callSite struct{ pos token.Pos }
	var conditionalSites []callSite
	var allSites []callSite

	// Collect all calls to <pgAlias>.<funcName> anywhere in the file.
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if !pgAliases[pkgIdent.Name] {
			return true
		}
		if sel.Sel.Name == funcName {
			allSites = append(allSites, callSite{call.Pos()})
		}
		return true
	})

	if len(allSites) == 0 {
		return false
	}

	// Collect calls to <pgAlias>.<funcName> that ARE inside a postgres if-body.
	ast.Inspect(file, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if !conditionMentionsPostgres(ifStmt.Cond) {
			return true
		}
		ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pgAliases[pkgIdent.Name] && sel.Sel.Name == funcName {
				conditionalSites = append(conditionalSites, callSite{call.Pos()})
			}
			return true
		})
		return true
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
