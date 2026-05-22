// INVARIANT: USERREPO-METHOD-SET-FROZEN-01
//
// USERREPO-METHOD-SET-FROZEN-01 — ports.UserRepository 方法集合 + 每方法签名
// 精确锁定。拆分 generic Update(*User) 为 UpdateProfile / UpdateLockState /
// UpdatePasswordResetFlag 三窄方法后，任何回退到 generic Update 形态、漏掉
// 新方法、改窄方法签名（包括把 *string 改回 string）、或为接口加 embedded
// sub-interface，本 archtest 在 CI 直接 fail。
//
// AI-rebust 评级：Medium（AST type-aware interface direct method set + signature
// 串精确匹配，与 CELL-IFACE-ISP-METHODSETS-01 同范式；Go type system 在
// interface method-set + signature 锁定问题上能达到的上限）。caller-facing
// method signature 维度是 Hard——generic Update(*User) 删除后、`*string` →
// `string` 签名退化、UpdateLockState 加 bool 参数 等等任何回归调用是编译
// 错误（caller 写不出来）。
//
// 盲区清单：
//   - loadInterfaceType only finds top-level type declarations; type aliases
//     and method-set composed via embedding are not covered by directMethodNames
//     (embedding 由本测试 explicit ban，避免悄悄绕过签名锁)。
//   - funcTypeString 仅序列化 *ast.Ident / *ast.SelectorExpr / *ast.StarExpr /
//     *ast.Ellipsis / nested *ast.FuncType 等常见形态；未来若 port 出现 generic
//     type parameters (Go 1.18+) 或 channel / map / slice 直接参数，须扩展 helper
//     并补 archtest case。当前 port surface 不含这些形态。
//
// Reverse self-test loads RoleRepository (sibling interface) and confirms its
// method set is NOT equal to UserRepository — validates loadInterfaceType
// can distinguish interfaces, blocking silent "any interface satisfies" false
// passes.
//
// ref: docs/architecture/202605222309-adr-user-repo-narrow-write-methods.md
// ref: github.com/ory/kratos/identity/pool.go UpdateIdentityColumns pattern
package archtest

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// expectedUserRepoMethodSignatures is the canonical (method name → normalized
// signature string) map for cells/accesscore/internal/ports.UserRepository
// after the issue #828 split. Signature string format: "(<params>) <results>",
// where each param is "<name> <type>" (or "<name1>, <name2> <type>" for shared
// type) and results use the same form. Multiple results are wrapped in `(...)`.
//
// Adding/removing methods OR changing any signature here is a contract change
// that must be paired with an ADR amendment (ADR 202605222309).
var expectedUserRepoMethodSignatures = map[string]string{
	"BumpAuthzEpoch":          "(ctx context.Context, userID string) (newEpoch int64, err error)",
	"Create":                  "(ctx context.Context, user *domain.User) error",
	"Delete":                  "(ctx context.Context, id string) error",
	"GetByID":                 "(ctx context.Context, id string) (*domain.User, error)",
	"GetByIDForUpdate":        "(ctx context.Context, id string) (*domain.User, error)",
	"GetByUsername":           "(ctx context.Context, username string) (*domain.User, error)",
	"GetByUsernameForUpdate":  "(ctx context.Context, username string) (*domain.User, error)",
	"UpdateLockState":         "(ctx context.Context, userID string, status domain.UserStatus, now time.Time) error",
	"UpdateLockoutFields":     "(ctx context.Context, user *domain.User) error",
	"UpdatePassword":          "(ctx context.Context, userID string, newHash string, resetRequired bool, expectedPasswordVersion int64) (newVersion int64, err error)",
	"UpdatePasswordResetFlag": "(ctx context.Context, userID string, required bool, now time.Time) error",
	"UpdateProfile":           "(ctx context.Context, userID string, name, email *string, now time.Time) (*domain.User, error)",
}

// TestUserRepoMethodSetFrozen verifies USERREPO-METHOD-SET-FROZEN-01: method
// names AND signatures match exactly, and the interface declares no embedded
// sub-interfaces.
func TestUserRepoMethodSetFrozen(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	iface := loadInterfaceType(t, root, "cells/accesscore/internal/ports", "UserRepository")
	if iface == nil {
		t.Fatal("USERREPO-METHOD-SET-FROZEN-01: UserRepository interface not found in cells/accesscore/internal/ports")
	}

	// Explicit embedded-interface ban — keeps the signature map authoritative.
	if embedded := embeddedTypeNames(iface); len(embedded) > 0 {
		t.Errorf("USERREPO-METHOD-SET-FROZEN-01: UserRepository must not embed sub-interfaces; got %v", embedded)
	}

	gotSigs := interfaceMethodSignatures(iface)
	gotNames := make([]string, 0, len(gotSigs))
	for name := range gotSigs {
		gotNames = append(gotNames, name)
	}
	sort.Strings(gotNames)

	wantNames := make([]string, 0, len(expectedUserRepoMethodSignatures))
	for name := range expectedUserRepoMethodSignatures {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)

	if !equalStringSlices(gotNames, wantNames) {
		t.Errorf("USERREPO-METHOD-SET-FROZEN-01: method names = %v, want exactly %v",
			gotNames, wantNames)
		return
	}

	for _, name := range wantNames {
		want := expectedUserRepoMethodSignatures[name]
		if got := gotSigs[name]; got != want {
			t.Errorf("USERREPO-METHOD-SET-FROZEN-01: method %s signature drift:\n  got:  %s\n  want: %s",
				name, got, want)
		}
	}
}

// TestUserRepoMethodSetFrozen_BlindSpotReverseSelfTest validates the blind-spot
// declaration in the package godoc: loadInterfaceType is an AST-only scanner
// (top-level type declarations only). Confirms the scanner distinguishes
// UserRepository from sibling interfaces — loading RoleRepository must NOT
// yield the same method-name set, blocking silent "any interface satisfies"
// false passes.
func TestUserRepoMethodSetFrozen_BlindSpotReverseSelfTest(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	sibling := loadInterfaceType(t, root, "cells/accesscore/internal/ports", "RoleRepository")
	if sibling == nil {
		t.Skip("USERREPO-METHOD-SET-FROZEN-01 blind-spot self-test: " +
			"RoleRepository not found in cells/accesscore/internal/ports; skipping reverse self-test")
		return
	}

	siblingMethods := directMethodNames(sibling)
	sort.Strings(siblingMethods)

	want := make([]string, 0, len(expectedUserRepoMethodSignatures))
	for name := range expectedUserRepoMethodSignatures {
		want = append(want, name)
	}
	sort.Strings(want)

	if equalStringSlices(siblingMethods, want) {
		t.Errorf("USERREPO-METHOD-SET-FROZEN-01 blind-spot reverse self-test: "+
			"RoleRepository has the same method set as expectedUserRepoMethodSignatures (%v); "+
			"loadInterfaceType cannot distinguish UserRepository from RoleRepository — "+
			"the primary test may be producing false passes", siblingMethods)
	}
}

// interfaceMethodSignatures returns (method name → normalized signature string)
// for each directly declared method in an interface. Embedded fields are skipped
// here; the primary test asserts the embedded list is empty.
func interfaceMethodSignatures(iface *ast.InterfaceType) map[string]string {
	out := map[string]string{}
	if iface.Methods == nil {
		return out
	}
	for _, field := range iface.Methods.List {
		if len(field.Names) == 0 {
			continue // embedded sub-interface — caught by embeddedTypeNames
		}
		fnType, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		sig := funcTypeString(fnType)
		for _, n := range field.Names {
			out[n.Name] = sig
		}
	}
	return out
}

// funcTypeString serializes an *ast.FuncType to a normalized string of the
// form "(<params>) <results>". Multi-result methods use "(<r1>, <r2>)";
// single result is bare. Params and results preserve parameter names (names
// are part of the contract since callers rely on positional + documentation
// alignment).
func funcTypeString(ft *ast.FuncType) string {
	var b strings.Builder
	b.WriteString("(")
	b.WriteString(fieldListString(ft.Params))
	b.WriteString(")")
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return b.String()
	}
	results := fieldListString(ft.Results)
	multi := false
	if len(ft.Results.List) > 1 {
		multi = true
	} else if len(ft.Results.List[0].Names) > 1 {
		multi = true
	} else if len(ft.Results.List[0].Names) == 1 {
		// Named single result still rendered as bare (no surrounding parens)
		// matches Go source style for `(newEpoch int64, err error)` 2-result vs
		// `(int64, error)` unnamed — caller distinguishes by `multi`.
		multi = false
	}
	b.WriteString(" ")
	if multi {
		b.WriteString("(")
		b.WriteString(results)
		b.WriteString(")")
	} else {
		b.WriteString(results)
	}
	return b.String()
}

// fieldListString serializes an *ast.FieldList to comma-separated entries.
// Each entry is "<name1>, <name2> <type>" when multiple names share a type,
// otherwise "<name> <type>" or bare "<type>" when nameless.
func fieldListString(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		typeStr := typeExprString(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typeStr)
			continue
		}
		names := make([]string, 0, len(f.Names))
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typeStr)
	}
	return strings.Join(parts, ", ")
}

// typeExprString is a focused-scope serializer for the AST forms reachable in
// the UserRepository surface today. Extending support requires a new case AND
// a meta-archtest entry that pins what's covered (see blind-spot list above).
func typeExprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return typeExprString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + typeExprString(e.X)
	case *ast.Ellipsis:
		return "..." + typeExprString(e.Elt)
	case *ast.FuncType:
		return "func" + funcTypeString(e)
	}
	return fmt.Sprintf("<unsupported-type:%T>", expr)
}
