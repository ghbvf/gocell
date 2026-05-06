package archtest

// goose_session_locker_test.go enforces GOOSE-SESSION-LOCKER-01: every
// goose.NewProvider call site under adapters/postgres/ MUST configure a
// SessionLocker via goose.WithSessionLocker. Without it, concurrent Up() calls
// race on the schema_migrations table (GitHub #21,
// POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01).
//
// Allowlist: schema_guard.go is exempt — its goose.NewProvider is used only by
// VerifyExpectedVersion's read-only GetDBVersion path; advisory locks add no
// value on a read-only readiness probe and would create connection contention.
// The mutating Up/Down path lives in migrator.go and MUST hold the lock.
//
// AST strategy:
//   1. parser.ParseDir adapters/postgres/ (skip subdirs, _test.go, allowlist)
//   2. for every CallExpr matching goose.NewProvider(...)
//   3. assert at least one of its variadic args is goose.WithSessionLocker(...)
//
// ref: pressly/goose lock/postgres.go pg_try_advisory_lock + retry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleGooseSessionLocker01 = "GOOSE-SESSION-LOCKER-01"

// gooseSessionLockerAllowlist contains files (relative to adapters/postgres/)
// where goose.NewProvider is intentionally invoked without WithSessionLocker.
// These are read-only paths that do not mutate schema_migrations.
var gooseSessionLockerAllowlist = map[string]string{
	"schema_guard.go": "VerifyExpectedVersion uses provider.GetDBVersion only (read-only)",
}

func TestGooseSessionLocker01(t *testing.T) {
	root := findModuleRoot(t)
	pgDir := filepath.Join(root, "adapters", "postgres")

	type violation struct {
		file string
		line int
	}
	var violations []violation

	walkErr := filepath.WalkDir(pgDir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if d.Name() == "migrations" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		base := filepath.Base(path)
		if _, allow := gooseSessionLockerAllowlist[base]; allow {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}

		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !isGooseNewProvider(call) {
				return true
			}
			if hasGooseWithSessionLocker(call.Args) {
				return true
			}
			pos := fset.Position(call.Pos())
			violations = append(violations, violation{
				file: filepath.ToSlash(rel),
				line: pos.Line,
			})
			return true
		})
		return nil
	})
	require.NoError(t, walkErr, "walking adapters/postgres/")

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleGooseSessionLocker01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  goose.NewProvider must include goose.WithSessionLocker(lock.NewPostgresSessionLocker())", v.file, v.line)
		}
	}
	assert.Empty(t, violations,
		"%s: every mutating goose.NewProvider in adapters/postgres/ must include "+
			"goose.WithSessionLocker; concurrent Up() races without it "+
			"(GH #21 / POSTGRES-MIGRATOR-LOCK-ORDER-REGRESSION-01)",
		ruleGooseSessionLocker01)
}

func isGooseNewProvider(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "NewProvider" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "goose"
}

func hasGooseWithSessionLocker(args []ast.Expr) bool {
	for _, arg := range args {
		call, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if sel.Sel.Name != "WithSessionLocker" {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "goose" {
			return true
		}
	}
	return false
}
