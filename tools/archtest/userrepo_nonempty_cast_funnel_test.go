// INVARIANT: USERREPO-NONEMPTY-CAST-FUNNEL-01
//
// USERREPO-NONEMPTY-CAST-FUNNEL-01 — `domain.NonEmpty(_)` 显式转换是 NonEmpty
// 验证 funnel (NewNonEmpty / UnmarshalJSON) 的最后旁路通道。Go 类型系统在
// type-renamed string 上无法禁掉 explicit conversion，因此本 archtest 锁定
// 该 callsite 形态的 file allowlist：
//
//   - corecells/accesscore/internal/domain/nonempty.go       (NonEmpty 类型自身)
//   - corecells/accesscore/slices/identitymanage/handler.go  (nonEmptyPtr 桥接)
//   - corecells/accesscore/internal/ports/conformance/conformance.go (test-helper
//     pkg 的非 _test.go 但仅被 *_test.go import，等价测试侧)
//   - *_test.go 任意路径 (fixture)
//
// AI-robust 评级：Medium 下游 (downstream Hard 形态：callee identity + path
// allowlist 由 AST 静态匹配，绕过须改本 archtest 同 PR)；上游 Medium (Go
// type-renamed string 的 cast 在 type system 上不可禁，只能 archtest 兜底)。
// 按 ai-robust.md "Funnel 双向锁评级 / 允许 Medium 上游 + Hard 下游的过渡
// 形态" 暂留——升级路径需要把 NonEmpty 改为 unexported struct + 私有构造，
// 代价超过本 PR 范围。
//
// 盲区清单：
//   - Bare ident `NonEmpty(s)` 在 domain 包内部不需要 selector 前缀；本
//     archtest 通过 file path allowlist 兜底——domain 包仅 nonempty.go 在
//     allowlist 内，其他 *.go 出现 bare `NonEmpty(_)` 也会被 fail。
//   - Reflect path (`reflect.ValueOf(s).Convert(NonEmpty type)`)：本 archtest
//     不覆盖；codebase 内无此用法 (grep-verified)。
//
// ref: ai-robust.md "string-typed concept funnel" + "Funnel 双向锁评级"
// ref: corecells/accesscore/internal/domain/nonempty.go
// ref: docs/architecture/202605222309-adr-user-repo-narrow-write-methods.md §4
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// nonEmptyCastAllowlist enumerates the production file paths (module-root
// relative) where `domain.NonEmpty(_)` or bare `NonEmpty(_)` cast is permitted.
// Adding a new entry requires the new callsite's necessity to be justified
// alongside the archtest edit (typically alongside an ADR §4 row update).
var nonEmptyCastAllowlist = map[string]struct{}{
	"corecells/accesscore/internal/domain/nonempty.go":               {},
	"corecells/accesscore/slices/identitymanage/handler.go":          {},
	"corecells/accesscore/internal/ports/conformance/conformance.go": {},
}

// TestUserRepoNonEmptyCastFunnel verifies USERREPO-NONEMPTY-CAST-FUNNEL-01:
// no production file outside the allowlist contains a `domain.NonEmpty(_)` or
// bare `NonEmpty(_)` cast.
func TestUserRepoNonEmptyCastFunnel(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := scanner.DirsScope(root, []string{
		"cells",
		"runtime",
		"kernel",
		"adapters",
		"pkg",
		"cmd",
	})
	files, err := scope.Files()
	if err != nil {
		t.Fatalf("scanner.DirsScope: %v", err)
	}

	var violations []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue // *_test.go anywhere is allowlisted
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("filepath.Rel(%s): %v", path, err)
		}
		if _, ok := nonEmptyCastAllowlist[rel]; ok {
			continue
		}
		hits := findNonEmptyCastSites(t, path)
		for _, h := range hits {
			violations = append(violations, rel+":"+h)
		}
	}

	if len(violations) > 0 {
		t.Errorf("USERREPO-NONEMPTY-CAST-FUNNEL-01: %d disallowed `(domain.)NonEmpty(_)` cast(s) outside funnel allowlist:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// findNonEmptyCastSites parses path and returns "line:col" positions of each
// CallExpr where Fun is either `domain.NonEmpty` (SelectorExpr) or bare
// `NonEmpty` (Ident).
func findNonEmptyCastSites(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
	var hits []string
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isNonEmptyCast(call) {
			return
		}
		p := fset.Position(call.Pos())
		hits = append(hits, fmt.Sprintf("%d:%d", p.Line, p.Column))
	})
	return hits
}

// isNonEmptyCast returns true when call.Fun is `domain.NonEmpty` (selector) or
// bare `NonEmpty` (ident).
func isNonEmptyCast(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		x, ok := fn.X.(*ast.Ident)
		if !ok {
			return false
		}
		return x.Name == "domain" && fn.Sel.Name == "NonEmpty"
	case *ast.Ident:
		return fn.Name == "NonEmpty"
	}
	return false
}
