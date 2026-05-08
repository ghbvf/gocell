// Package archtest — builtin role invariants.
//
// INVARIANT: BUILTIN-ROLE-ID-NAME-EQ-01
//
// 不能 funnel 的理由：role.id 与 role.Name 是 PG schema（019_roles.sql 的
// partial UNIQUE）与 Go domain（runtime/auth/roles.go 的 string 常量）两侧的
// 属性，funnel 不到单源；type system 也无法表达"两个独立 string 字段必须相等"。
// 平铺 archtest 兜底。
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuiltinRoleIDNameEq enforces BUILTIN-ROLE-ID-NAME-EQ-01.
//
// 约束背景：
//   - 019_roles.sql 以 partial UNIQUE 索引 `WHERE role_id = 'admin'` 保护
//     single-admin 不变量，绑定的是字符串字面量 'admin'。
//   - sessionmint 等模块通过 role.Name（字符串）识别 admin 角色。
//   - runtime/auth/roles.go 的 const RoleAdmin = "admin" 同时被用作 role.id
//     与 role.Name 的比较值；两者相等是隐式约定。
//
// 本测试验证：
//  1. runtime/auth/roles.go 中存在至少一个 Role* 前缀的 string 常量。
//  2. RoleAdmin 常量值必须为 "admin"（与 019_roles.sql partial UNIQUE 字面量一致）。
//  3. 所有 Role* 常量值必须满足 ^[a-z][a-z0-9_-]*$——小写、可作 SQL 标识符段，
//     确保 id 与 name 在格式层面不会出现大小写或特殊字符导致的静默不一致。
//
// 如果未来需要引入 id≠name 的 builtin role，必须同时：
//   - 修改 019_roles.sql 的 partial UNIQUE 表达（从字面量改为显式列比较）；
//   - 删除或放宽本测试中对应的断言；
//   - 在 ADR docs/architecture/202605081600-adr-pg-accesscore-locking.md 中
//     记录决策变更。
//
// INVARIANT: BUILTIN-ROLE-ID-NAME-EQ-01
func TestBuiltinRoleIDNameEq(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	rolesFile := filepath.Join(root, "runtime", "auth", "roles.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rolesFile, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "BUILTIN-ROLE-ID-NAME-EQ-01: failed to parse %s", rolesFile)

	// 收集所有 package-level const Role* = "..." 声明
	found := map[string]string{} // constName → value
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Role") {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				// BasicLit.Value 包含两端引号，如 `"admin"`，去掉引号取纯文本。
				value := strings.Trim(bl.Value, `"`)
				found[name.Name] = value
			}
		}
	}

	// 断言 1：至少存在一个 Role* 常量，确保扫描本身有效
	require.NotEmptyf(t, found,
		"BUILTIN-ROLE-ID-NAME-EQ-01: no Role* string constants found in %s — "+
			"invariant scope broken; if the file was moved, update this test",
		rolesFile)

	// 断言 2：RoleAdmin 必须等于 "admin"（与 019_roles.sql partial UNIQUE 字面量绑定）
	adminVal, hasAdmin := found["RoleAdmin"]
	assert.Truef(t, hasAdmin,
		"BUILTIN-ROLE-ID-NAME-EQ-01: RoleAdmin constant missing from %s — "+
			"single-admin partial UNIQUE in 019_roles.sql binds to literal 'admin'",
		rolesFile)
	if hasAdmin {
		assert.Equalf(t, "admin", adminVal,
			"BUILTIN-ROLE-ID-NAME-EQ-01: RoleAdmin = %q, want \"admin\" — "+
				"019_roles.sql partial UNIQUE WHERE role_id = 'admin' binds to this literal; "+
				"changing the value requires updating the migration and this test",
			adminVal)
	}

	// 断言 3：所有 Role* 常量值必须是小写 SQL 安全标识符
	// 格式：首字符 [a-z]，其余 [a-z0-9_-]*
	// 原因：role.id（PG 列）与 role.Name（Go 域）均使用同一常量值；
	// 大写或特殊字符会在 SQL UNIQUE 索引与 Go 字符串比较之间引入不一致风险。
	validFormat := regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	for constName, constVal := range found {
		assert.Truef(t, validFormat.MatchString(constVal),
			"BUILTIN-ROLE-ID-NAME-EQ-01: const %s = %q does not match SQL-safe lowercase "+
				"identifier pattern ^[a-z][a-z0-9_-]*$ — id/name divergence risk: "+
				"role.id (PG) and role.Name (Go) must be representable by the same literal",
			constName, constVal)
	}
}
