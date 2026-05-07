package archtest

// pg_repo_invariants_test.go enforces three PostgreSQL adapter invariants
// (theme: pg-repo), all funnel-unreachable (type system cannot express the
// constraint), so they are guarded here by AST inspection.
//
// Scan scope (both kept in sync via pgRepoScanRoots):
//   - adapters/postgres/                       — kernel/runtime adapters
//   - cells/<cell>/internal/adapters/postgres/ — cell-internal repos that
//     implement cell-specific ports. CLAUDE.md forbids cells/ from importing
//     adapters/, so PG implementations of cell-internal ports live under the
//     owning cell (precedent: configcore/auditcore).
//
// Invariants:
//   - PG-CONSTRUCTOR-MUST-FREE-01: no MustNew* constructors anywhere in scope
//   - PG-REPO-AMBIENT-TX-01: *_repo.go/*_store.go CRUD methods must not call
//     pool.Begin / pool.BeginTx directly; must use txRunner.RunInTx or
//     execCtx/queryRowCtx helpers
//   - PG-REPO-ROLLBACK-REDACT-01: functions referencing rollback / Rollback
//     must call pkg/redaction.RedactError

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

// pgRepoScanRoots returns the directories that should be walked by every PG
// repo invariant: the canonical adapters/postgres/ tree plus every
// cells/<cell>/internal/adapters/postgres/ tree present in the repo.
//
// CLAUDE.md forbids cells/ from importing adapters/, so PG implementations of
// cell-internal ports live under the owning cell. The two locations share the
// same invariants and must be treated as one scan surface.
func pgRepoScanRoots(t *testing.T, root string) []string {
	t.Helper()
	roots := []string{filepath.Join(root, "adapters", "postgres")}

	cellsDir := filepath.Join(root, "cells")
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		// cells/ missing in some test layouts is acceptable; just report
		// adapters/postgres/.
		return roots
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(cellsDir, e.Name(), "internal", "adapters", "postgres")
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.IsDir() {
			roots = append(roots, candidate)
		}
	}
	return roots
}

const rulePGConstructorMustFree01 = "PG-CONSTRUCTOR-MUST-FREE-01"

// TestPGConstructorMustFree01 walks every PG repo scan root (see
// pgRepoScanRoots) and reports any exported MustNew* function declaration.
func TestPGConstructorMustFree01(t *testing.T) {
	root := findModuleRoot(t)
	scanRoots := pgRepoScanRoots(t, root)

	type violation struct {
		file string
		line int
		name string
	}
	var violations []violation

	for _, pgDir := range scanRoots {
		err := filepath.WalkDir(pgDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				// skip sub-directories that aren't the postgres package itself
				if d.Name() == "migrations" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}

			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
			if parseErr != nil {
				return parseErr
			}

			rel, _ := filepath.Rel(root, path)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := fd.Name.Name
				// exported MustNew* at package level (no receiver)
				if fd.Recv != nil {
					continue
				}
				if !strings.HasPrefix(name, "MustNew") {
					continue
				}
				pos := fset.Position(fd.Pos())
				violations = append(violations, violation{
					file: filepath.ToSlash(rel),
					line: pos.Line,
					name: name,
				})
			}
			return nil
		})
		require.NoError(t, err, "walking %s", pgDir)
	}

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", rulePGConstructorMustFree01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s — MustNew* constructors are banned in PG repo scope (B2-A-11)", v.file, v.line, v.name)
		}
	}
	assert.Empty(t, violations,
		"%s: PG repo scope must not export MustNew* constructors; use error-first NewXxx instead (B2-A-11)",
		rulePGConstructorMustFree01)
}

// ---------------------------------------------------------------------------
// PG-REPO-AMBIENT-TX-01
// ---------------------------------------------------------------------------

const rulePGRepoAmbientTx01 = "PG-REPO-AMBIENT-TX-01"

// TestPGRepoAmbientTx01 enforces that CRUD methods on *Repository / *Store
// types in adapters/postgres/*_repo.go and *_store.go files do not call
// pool.Begin / pool.BeginTx directly on a struct field named "pool".
// All transaction management must go through txRunner.RunInTx or the
// execCtx / queryRowCtx helper wrappers.
//
// INVARIANT: PG-REPO-AMBIENT-TX-01
// 理由：funnel 不可达 — type system 无法区分"在 TxManager 管理的 context 里"vs
//
//	"直接 Begin"；codegen 无对应钩子。
//
// 守护：adapters/postgres/*_repo.go / *_store.go CRUD 方法体禁止 pool.Begin
//
//	/ pool.BeginTx 直接调用（必须走 txRunner.RunInTx 或 execCtx / queryRowCtx）。
//
// 豁免：tx_manager.go（实现 TxRunner 接口本身）。
func TestPGRepoAmbientTx01(t *testing.T) {
	root := findModuleRoot(t)
	scanRoots := pgRepoScanRoots(t, root)

	// allowlist: files that legitimately implement Begin themselves.
	allowlist := map[string]bool{
		"tx_manager.go": true,
	}

	type violation struct {
		file   string
		line   int
		method string
		call   string
	}
	var violations []violation

	for _, pgDir := range scanRoots {
		err := filepath.WalkDir(pgDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
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
			// Only inspect *_repo.go and *_store.go files.
			if !strings.HasSuffix(base, "_repo.go") && !strings.HasSuffix(base, "_store.go") {
				return nil
			}
			// Skip allowlisted files.
			if allowlist[base] {
				return nil
			}

			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
			if parseErr != nil {
				return parseErr
			}

			rel, _ := filepath.Rel(root, path)

			// Walk each method declaration with a pointer receiver.
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || fd.Body == nil {
					continue
				}

				methodName := fd.Name.Name

				// Inspect all call expressions in the method body.
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					// Check if method name is Begin or BeginTx.
					if sel.Sel.Name != "Begin" && sel.Sel.Name != "BeginTx" {
						return true
					}
					// Check if the receiver is a field access on "s" or "r"
					// whose field name is "pool".
					fieldAccess, ok := sel.X.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if fieldAccess.Sel.Name != "pool" {
						return true
					}
					pos := fset.Position(call.Pos())
					violations = append(violations, violation{
						file:   filepath.ToSlash(rel),
						line:   pos.Line,
						method: methodName,
						call:   "." + fieldAccess.Sel.Name + "." + sel.Sel.Name,
					})
					return true
				})
			}
			return nil
		})
		require.NoError(t, err, "walking %s for PG-REPO-AMBIENT-TX-01", pgDir)
	}

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", rulePGRepoAmbientTx01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  method=%s call=%s — use txRunner.RunInTx or execCtx/queryRowCtx instead",
				v.file, v.line, v.method, v.call)
		}
	}
	assert.Empty(t, violations,
		"%s: *_repo.go/*_store.go CRUD methods must not call pool.Begin/pool.BeginTx directly; use txRunner.RunInTx or execCtx/queryRowCtx",
		rulePGRepoAmbientTx01)
}

// ---------------------------------------------------------------------------
// PG-REPO-ROLLBACK-REDACT-01
// ---------------------------------------------------------------------------

const rulePGRepoRollbackRedact01 = "PG-REPO-ROLLBACK-REDACT-01"

// TestPGRepoRollbackRedact01 enforces that every non-test Go file in
// adapters/postgres/ that contains a function body referencing "rollback" or
// "Rollback" also calls pkg/redaction.RedactError somewhere in that same
// function body. This ensures rollback error paths are always sanitized before
// being written to structured logs.
//
// INVARIANT: PG-REPO-ROLLBACK-REDACT-01
// 理由：funnel 不可达 — slog.Error / errcode.Wrap 是普通函数调用，type system 无法
//
//	区分 plain error 与 redacted error；codegen 无对应钩子。
//
// 守护：adapters/postgres/*.go 含 rollback / Rollback 的函数必须包含 pkg/redaction
//
//	的 RedactError 调用，确保 rollback 错误经脱敏后写日志。
//
// 范式：PR#388 PG-CONSTRUCTOR-MUST-FREE-01 + PR#395 ADR span-redact。
func TestPGRepoRollbackRedact01(t *testing.T) {
	root := findModuleRoot(t)
	scanRoots := pgRepoScanRoots(t, root)

	type violation struct {
		file   string
		line   int
		fn     string
		reason string
	}
	var violations []violation

	for _, pgDir := range scanRoots {
		err := filepath.WalkDir(pgDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
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

			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
			if parseErr != nil {
				return parseErr
			}

			rel, _ := filepath.Rel(root, path)

			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}

				fnName := fd.Name.Name
				fnPos := fset.Position(fd.Pos())

				// Check whether this function body references "rollback" / "Rollback".
				hasRollback := funcBodyContainsRollback(fd.Body)
				if !hasRollback {
					continue
				}

				// Function body references rollback — ensure RedactError is also called.
				hasRedact := funcBodyContainsRedactError(fd.Body)
				if !hasRedact {
					violations = append(violations, violation{
						file:   filepath.ToSlash(rel),
						line:   fnPos.Line,
						fn:     fnName,
						reason: "contains rollback but no redaction.RedactError call",
					})
				}
			}
			return nil
		})
		require.NoError(t, err, "walking %s for PG-REPO-ROLLBACK-REDACT-01", pgDir)
	}

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", rulePGRepoRollbackRedact01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  func=%s — %s", v.file, v.line, v.fn, v.reason)
		}
	}
	assert.Empty(t, violations,
		"%s: functions in adapters/postgres/ referencing rollback/Rollback must call redaction.RedactError (PR#388/PR#395 pattern)",
		rulePGRepoRollbackRedact01)
}

// funcBodyContainsRollback reports whether the given function body AST node
// contains a Rollback / rollback identifier (function or method call), AND
// also calls slog.Error or slog.Warn within the same body — meaning there is
// a log path that could leak unsanitised rollback error text.
//
// This deliberately excludes:
//   - String literals containing "rollback" (e.g. errcode.Wrap message text)
//   - Rollback calls whose error is silently discarded (no log path)
func funcBodyContainsRollback(body *ast.BlockStmt) bool {
	hasRollbackCall := false
	hasSlogErrorOrWarn := false

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name := fn.Sel.Name
			// Rollback / rollback method calls (tx.Rollback, tx.Exec("ROLLBACK..."))
			if strings.Contains(strings.ToLower(name), "rollback") {
				hasRollbackCall = true
			}
			// slog.Error / slog.Warn at the call level
			if name == "Error" || name == "Warn" {
				if recv, ok := fn.X.(*ast.Ident); ok && recv.Name == "slog" {
					hasSlogErrorOrWarn = true
				}
			}
		case *ast.Ident:
			if strings.Contains(strings.ToLower(fn.Name), "rollback") {
				hasRollbackCall = true
			}
		}
		return true
	})

	return hasRollbackCall && hasSlogErrorOrWarn
}

// funcBodyContainsRedactError reports whether the given function body contains
// a call to redaction.RedactError (selector expression: x.RedactError).
func funcBodyContainsRedactError(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "RedactError" {
			found = true
		}
		return !found
	})
	return found
}
