//go:build archtest

// INVARIANT: RECONSTITUTE-USER-CALLER-01
//
// AI-robust: Medium
//   - callee 解析: typeseval.ResolvePackageRef *types.PkgName identity → type-aware
//   - caller 鉴定: file path prefix → string convention
//   - 综合: Medium 天花板
//
// 闭环 funnel：与 DOMAIN-AUTHZ-FIELD-PRIVATE-01 形成上下游对锁：
//   - 下游 Hard (DOMAIN-AUTHZ-FIELD-PRIVATE-01)：User.status/passwordResetRequired/authzEpoch
//     unexported，跨包写是编译错误
//   - 上游 Medium（本规则）：ReconstituteUser 只能被 mem/ / adapters/postgres/ / domain/
//     / *_test.go 调用，防止未来 slice 通过 ReconstituteUser 绕过 SetStatus funnel
//
// Hard 升级路径：sealed token 模式（调用方必须持有 domain 包内定义的令牌才能调用）。
//
// 盲区清单（无法被 typeseval.ResolvePackageRef 静态识别，依赖反向自检确认不在 prod AST）：
//
//  1. method-value 赋值：fn := domain.ReconstituteUser; fn(...)
//     第二个 fn(...) CallExpr 的 Fun 是 *ast.Ident，不是 *ast.SelectorExpr，
//     info.Uses 不会给出 *types.PkgName，ResolvePackageRef 返回 false。
//     反向自检：TestReconstituteUser_BlindSpot_NoMethodValueOrReflectInProd
//     断言该形态不出现在 prod AST。
//
//  2. reflect.ValueOf(domain.ReconstituteUser).Call(...)
//     完全 AST 不可见。反向自检同上。
//
//  3. unsafe.Pointer 直接调用函数地址 — 当前不适用（ReconstituteUser 是普通 Go 函数，
//     不是方法，unsafe.Pointer 调用函数地址极为罕见）。反向自检同上守卫 unsafe import。
//
//  4. generic 包装 callReconstitute[T](...) — 当前 ReconstituteUser 非泛型；
//     若未来改为泛型，ResolvePackageRef 对 *ast.IndexExpr（泛型实例化）返回 false，
//     届时需扩展。反向自检：TestReconstituteUser_BlindSpot_NoMethodValueOrReflectInProd
//     间接守卫（当前产物不含泛型包装器）。

package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ─── Rule: RECONSTITUTE-USER-CALLER-01 ──────────────────────────────────────

// TestReconstituteUserCallerAllowlist enforces RECONSTITUTE-USER-CALLER-01:
// every call to domain.ReconstituteUser in non-test production code must
// originate from {mem/, adapters/postgres/, domain/}. Slice business logic
// must NOT rehydrate User aggregates directly; it must go through the
// repository interface instead (which internally calls ReconstituteUser from
// the allowed persistence boundary).
//
// Scanning scope: cells/accesscore/... and cmd/... — the layers where a slice
// writer could plausibly add a direct call. runtime/ and adapters/ do not
// know about domain.User and are not scanned.
//
// RED fixture: cells/accesscore/internal/domain/testdata/reconstitute_user_caller_red/
// contains a deliberate violation (call from a non-allowlisted path); verified
// by TestReconstituteUserCallerAllowlist_REDFixture.
func TestReconstituteUserCallerAllowlist(t *testing.T) {
	t.Parallel()
	Report(t, ruleReconstituteUserCaller01, CheckReconstituteUserCallerAllowlist01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestReconstituteUserCallerAllowlist_REDFixture verifies the double-lock:
// the scanner must capture exactly 1 violation in the RED fixture package,
// proving the scanner is not silently misconfigured.
//
// The fixture (cells/accesscore/internal/domain/testdata/reconstitute_user_caller_red/)
// calls domain.ReconstituteUser from a path that is NOT in the production
// allowlist and is NOT a _test.go file. A count of 0 means the scanner is dead;
// a count > 1 means the fixture has grown unexpectedly.
func TestReconstituteUserCallerAllowlist_REDFixture(t *testing.T) {
	t.Parallel()

	var found int
	Run(t, Typed(TypedOpts{Tests: false},
		[]string{"./cells/accesscore/internal/domain/testdata/reconstitute_user_caller_red"}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)

				found += len(scanReconstituteViolationsPass(p, file, rel))
			}
			return nil
		})

	assert.Equal(t, 1, found,
		"RECONSTITUTE-USER-CALLER-01 RED fixture self-check: expected exactly 1 violation, got %d. "+
			"If 0: the scanner is dead (wrong pkg path, wrong function name, or fixture not loaded). "+
			"If >1: the fixture has grown unexpectedly. Repair to exactly 1 call site.",
		found)
}

// TestReconstituteUser_BlindSpot_NoMethodValueOrReflectInProd asserts that the
// two undetectable call forms for ReconstituteUser do NOT appear in production
// code, confirming the blind spots are not exercised.
//
// Blind spot 1 — method-value assignment:
//
//	fn := domain.ReconstituteUser
//	result, err := fn(params)  // Fun=*ast.Ident, ResolvePackageRef returns false
//
// Blind spot 2 — reflect invocation:
//
//	reflect.ValueOf(domain.ReconstituteUser).Call(...)
//
// If either pattern appeared, the main scanner would miss the actual call.
// This test verifies their absence so the "blind spot is not present" premise
// remains valid.
//
// Scanner: AST-only search for *ast.SelectorExpr with Sel.Name == "ReconstituteUser"
// that is NOT in the CallExpr.Fun position (potential method-value), and for
// MethodByName("ReconstituteUser") patterns.
func TestReconstituteUser_BlindSpot_NoMethodValueOrReflectInProd(t *testing.T) {
	t.Parallel()

	var allDiags []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false},
		[]string{"./cells/accesscore/...", "./cmd/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			var diags []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				callFunPositions := map[ast.Node]bool{}
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						callFunPositions[sel] = true
					}
				})
				EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
					if sel.Sel == nil || sel.Sel.Name != reconstituteUserName {
						return
					}
					if callFunPositions[sel] {
						return
					}
					line := p.Fset.Position(sel.Pos()).Line
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: line,
						Message: fmt.Sprintf(
							"method-value assignment of domain.ReconstituteUser detected — " +
								"RECONSTITUTE-USER-CALLER-01 would miss the deferred call site",
						),
					})
				})

				for _, hit := range scanReflectStringArgCalls(p, file, reflectMethodByName,
					func(n string) bool { return n == reconstituteUserName }) {
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: hit.Line,
						Message: fmt.Sprintf(
							"reflect.MethodByName(%q) detected — "+
								"RECONSTITUTE-USER-CALLER-01 cannot see reflect-based invocations",
							hit.Name,
						),
					})
				}
			}
			allDiags = append(allDiags, diags...)
			return nil
		})

	var violations []string
	for _, d := range allDiags {
		violations = append(violations, fmt.Sprintf("%s:%d: %s", d.Rel, d.Line, d.Message))
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"RECONSTITUTE-USER-CALLER-01 blind-spot: method-value assignment or reflect.MethodByName of "+
			"domain.ReconstituteUser found in non-test production code — "+
			"the archtest scanner would miss these call sites. "+
			"Refactor to use direct calls through the UserRepository interface.")
}
