//go:build archtest

// INVARIANT: BASELINE-ROLE-STRING-SINGLE-SOURCE-01
//
// archtest: BASELINE-ROLE-STRING-SINGLE-SOURCE-01
//
// # BASELINE-ROLE-STRING-SINGLE-SOURCE-01 (断言1 Hard + 断言2 Medium 组合)
//
// Platform role 字符串值（"admin" / "superadmin"）的唯一合法定义点是
// framework/runtime/auth/roles.go。本规则通过两条独立断言守卫这一单源：
//
// ## 断言 1 — 值集冻结（Hard：value-golden freeze）
//
// [TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01_Freeze] 扫描 roles.go，提取所有
// `const Role* = "字面量"` 声明，断言其构成集合恰好等于本文件中的
// [frozenPlatformRoles]（value-golden）。
//
// 同时断言 [rolePrefixPlatformAlias]（ROLE-PREFIX-NAMESPACED-01 中的平台 role
// 别名 map）与 [frozenPlatformRoles] 完全相等——这闭合了 ROLE-PREFIX-NAMESPACED-01
// godoc §盲区 中记录的「alias map 与 roles.go 手动同步、无 enforcement」盲区，
// 将同步关系升格为 CI-checked（Hard 范式：冻结集合相等断言）。
//
// 评级：Hard。roles.go 漂移 → reflect.DeepEqual 失败 → CI 红。
//
// ## 断言 2 — Funnel 扫描裸 role 值字面量（Medium：AST 扫描）
//
// [TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01] 扫描 production .go 文件（Tests:false），
// 检查是否存在直接使用平台 role 值字面量（"admin"/"superadmin"）的 BasicLit STRING
// 节点，仅允许 roles.go 本身包含这些值。
//
// 覆盖 baseline.go（adminOrSuperAdmin() 以常量引用，已合规）+ rowscope.go +
// deviceprincipal.go 中可能出现的 role 值漂移路径；也覆盖新引入的 composite-literal
// 路径（如 `[]string{"admin", "superadmin"}` 裸值）。
//
// 为什么是 Medium 而不是 Hard：
//
//   - abac.Condition.Values 是通用 []string，sealed Role type 在此位置依然需要
//     archtest 兜底（类型系统不可单独闭合）。
//   - 将 RoleAdmin/RoleSuperAdmin 改为专有 sealed 类型会波及 auth.Principal.Roles、
//     JWT 解析和整个 ABAC evaluator，超出 Cx-2 范围，违背 ADR 639 既定权衡。
//   - 运行时拼接 / 非字面量表达式无法被 AST 扫描捕获（已记录为盲区）。
//
// 评级：Medium（sanctioned Medium，非 Hard 代理）。
//
// # Anti-vacuity
//
// 断言2 production 扫描后验证扫到的文件数 > 0，且实际看到了 roles.go 文件
// （banned 值的定义源），确保扫描范围未悄然空洞化。
//
// # 盲区自检（charter §"强制盲区自检"）
//
//   - 扫描范围外的包不覆盖（如 adapters/ 中若引入 role 裸值不被此规则捕获）。
//   - 运行时拼接或非字面量表达式（fmt.Sprintf("admin")、concat）逃逸 AST 扫描。
//   - _test.go 文件中合法存在 "admin"/"superadmin"（构造 test principal），
//     Tests:false 将其排除——这是刻意设计，非盲区。
//
// # 关联
//
// ref: framework/runtime/auth/roles.go — platform role 单源（drift 由本守卫检测）。
// ref: tools/archtest/cell_id_pattern_single_source_test.go — 同类 const-ref 单源 + value-golden 范式。
// ref: tools/archtest/role_prefix_namespaced_test.go — alias map 同步盲区由断言1闭合。
// ref: docs/architecture/202606151430-639-adr-role-naming-convention.md — ADR 639，amendment 2026-06-19 #1915。
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const ruleBaselineRoleStringSingleSource01 = "BASELINE-ROLE-STRING-SINGLE-SOURCE-01"

// frozenPlatformRoles 是平台保留 role 的 {常量名: 值} 冻结集（value-golden）。
// 单源 = framework/runtime/auth/roles.go；本守卫断言该文件的 const Role* 集合恰等于此。
// 改/增/换任何平台 role 即触发失败，强制有意识更新本集合 + 复评所有镜像处。
var frozenPlatformRoles = map[string]string{
	"RoleAdmin":      "admin",
	"RoleSuperAdmin": "superadmin",
}

// rolesGoRelPath 是 roles.go 在模块内的逻辑路径（framework 模块剥掉 "framework/" 前缀）。
const rolesGoRelPath = "runtime/auth/roles.go"

// baselineRoleAllowRel 是允许包含平台 role 值字面量的文件（仅 roles.go 本身）。
const baselineRoleAllowRel = rolesGoRelPath

// TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01_Freeze 断言1：
// 扫描 roles.go 提取 const Role* 集合，断言与 frozenPlatformRoles 完全相等（Hard value-golden）。
// 同时断言 rolePrefixPlatformAlias（ROLE-PREFIX-NAMESPACED-01 同包 var）与 frozenPlatformRoles
// 相等——闭合 alias map↔roles.go 同步盲区。
func TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01_Freeze(t *testing.T) {
	t.Parallel()

	observed := make(map[string]string)
	var rolesGoSeen bool

	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./framework/runtime/auth"}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if rel != rolesGoRelPath {
					continue
				}
				rolesGoSeen = true
				extractRoleConsts(f, observed)
			}
			return nil
		})

	require.True(t, rolesGoSeen,
		"%s Freeze anti-vacuity: roles.go was not seen during scan (rel=%q); "+
			"verify Typed pattern './framework/runtime/auth' still resolves the file",
		ruleBaselineRoleStringSingleSource01, rolesGoRelPath)

	require.NotEmpty(t, observed,
		"%s Freeze anti-vacuity: no Role* const was extracted from roles.go; "+
			"check extractRoleConsts or the file structure",
		ruleBaselineRoleStringSingleSource01)

	if !reflect.DeepEqual(observed, frozenPlatformRoles) {
		t.Fatalf("%s Freeze: roles.go platform role set漂移。\n"+
			"observed:  %v\n"+
			"frozen:    %v\n"+
			"→ 更新 frozenPlatformRoles + rolePrefixPlatformAlias + 复评所有镜像处",
			ruleBaselineRoleStringSingleSource01, observed, frozenPlatformRoles)
	}

	if !reflect.DeepEqual(rolePrefixPlatformAlias, frozenPlatformRoles) {
		t.Fatalf("%s Freeze: rolePrefixPlatformAlias (ROLE-PREFIX-NAMESPACED-01) 与 frozenPlatformRoles 不同步。\n"+
			"rolePrefixPlatformAlias: %v\n"+
			"frozenPlatformRoles:     %v\n"+
			"→ 更新二者使其一致，并复评 ROLE-PREFIX-NAMESPACED-01 godoc",
			ruleBaselineRoleStringSingleSource01, rolePrefixPlatformAlias, frozenPlatformRoles)
	}
}

// extractRoleConsts 从 file 中提取所有顶层 const Role* = "string_literal" 声明。
// 结果写入 out（名→值）。
func extractRoleConsts(file *ast.File, out map[string]string) {
	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST {
			return
		}
		var lastValues []ast.Expr
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			values := vs.Values
			if values == nil {
				values = lastValues
			} else {
				lastValues = values
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Role") {
					continue
				}
				if i >= len(values) {
					continue
				}
				lit, ok := values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				v, ok := StringLitValue(lit)
				if !ok {
					continue
				}
				out[name.Name] = v
			}
		})
	})
}

// baselineRoleLiteralDiagnostics 是共享扫描核心，供 production 扫描和 RED fixture 复用。
// banned 是禁止出现的 role 值集合（来自 frozenPlatformRoles 的值），
// allow 是允许包含这些值的文件 rel 集合（production 扫描填入 roles.go；fixture 扫描为空）。
func baselineRoleLiteralDiagnostics(p *Pass, banned map[string]struct{}, allow map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for _, f := range p.Files {
		rel := p.Rel(f)
		if _, skip := allow[rel]; skip {
			continue
		}
		EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			v, ok := StringLitValue(lit)
			if !ok {
				return
			}
			if _, isBanned := banned[v]; !isBanned {
				return
			}
			line := p.Fset.Position(lit.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"%s: ABAC baseline / auth 派生路径禁止裸 role 值字面量 %q；"+
						"用 runtime/auth.RoleAdmin / RoleSuperAdmin 常量",
					ruleBaselineRoleStringSingleSource01, v,
				),
			})
		})
	}
	return diags
}

// bannedRoleValues 返回 frozenPlatformRoles 的值集合（set 形式）。
func bannedRoleValues() map[string]struct{} {
	out := make(map[string]struct{}, len(frozenPlatformRoles))
	for _, v := range frozenPlatformRoles {
		out[v] = struct{}{}
	}
	return out
}

// TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01 断言2：
// 扫描 production .go 文件（Tests:false），检测裸 role 值字面量（Medium AST funnel）。
// 仅 roles.go 本身允许包含这些值。期望 0 违规。
func TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01(t *testing.T) {
	t.Parallel()

	banned := bannedRoleValues()
	allow := map[string]struct{}{
		baselineRoleAllowRel: {},
	}

	var fileCount int
	var rolesGoSeen bool

	diags := Run(t,
		Typed(TypedOpts{Tests: false}, []string{
			"./framework/runtime/auth",
			"./corecells/accesscore/slices/authorizationdecide",
		}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				fileCount++
				if rel == rolesGoRelPath {
					rolesGoSeen = true
				}
			}
			return baselineRoleLiteralDiagnostics(p, banned, allow)
		})

	// Anti-vacuity: 确保扫描确实覆盖了 roles.go 和足够多的文件。
	if !rolesGoSeen {
		t.Errorf("%s anti-vacuity: roles.go (%q) 未被扫描到；"+
			"扫描范围可能已漂移，请检查 Typed patterns",
			ruleBaselineRoleStringSingleSource01, rolesGoRelPath)
	}
	if fileCount == 0 {
		t.Errorf("%s anti-vacuity: 扫描到的文件数为 0；"+
			"扫描范围已完全空洞化",
			ruleBaselineRoleStringSingleSource01)
	}

	Report(t, ruleBaselineRoleStringSingleSource01, diags)
}

// TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01_RedFixture 是 RED path 自检：
// 对 baselinerolefixture 跑同一 baselineRoleLiteralDiagnostics，验证 detector 有效。
// fixture 故意写了 2 条 banned 值（"admin" 和 "superadmin"），期望恰好命中 2 条。
func TestBASELINE_ROLE_STRING_SINGLE_SOURCE_01_RedFixture(t *testing.T) {
	t.Parallel()

	banned := bannedRoleValues()
	// fixture 扫描不设 allow（fixture 文件本身就是违规者）
	allow := map[string]struct{}{}

	var allDiags []Diagnostic
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{"./tools/archtest/internal/baselinerolefixture"}),
		func(p *Pass) []Diagnostic {
			d := baselineRoleLiteralDiagnostics(p, banned, allow)
			allDiags = append(allDiags, d...)
			return nil
		})

	require.Len(t, allDiags, 2,
		"%s RED fixture 自检失败：期望恰好 2 条违规（\"admin\" 和 \"superadmin\" 各一条），"+
			"实际得到 %d 条。<2 表示 detector 漏检；>2 表示误伤了非违规代码。",
		ruleBaselineRoleStringSingleSource01, len(allDiags))

	msgs := allDiags[0].Message + allDiags[1].Message
	require.Contains(t, msgs, `"admin"`,
		"%s RED fixture: 诊断信息中未包含 \"admin\"", ruleBaselineRoleStringSingleSource01)
	require.Contains(t, msgs, `"superadmin"`,
		"%s RED fixture: 诊断信息中未包含 \"superadmin\"", ruleBaselineRoleStringSingleSource01)
}
