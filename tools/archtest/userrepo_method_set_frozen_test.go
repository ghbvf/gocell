// invariants:
//   - INVARIANT: USERREPO-METHOD-SET-FROZEN-01
//
// USERREPO-METHOD-SET-FROZEN-01 — ports.UserRepository 方法集合精确锁定。
// 拆分 generic Update(*User) 为 UpdateProfile / UpdateLockState /
// UpdatePasswordResetFlag 三窄方法后，任何回退到 generic Update 形态或漏掉
// 新方法的修改本 archtest 在 CI 直接 fail。
//
// AI-rebust 评级：Medium（AST type-aware interface direct method set 精确
// 匹配，与 CELL-IFACE-ISP-METHODSETS-01 同范式；Go type system 在 interface
// method-set 锁定问题上能达到的上限）。caller-facing method signature 维度
// 是 Hard——generic Update(*User) 删除后任何回归调用是编译错误。
//
// ref: docs/architecture/202605222309-adr-user-repo-narrow-write-methods.md
// ref: github.com/ory/kratos/identity/pool.go UpdateIdentityColumns pattern
package archtest

import (
	"sort"
	"testing"
)

// expectedUserRepoMethods is the canonical method set for
// cells/accesscore/internal/ports.UserRepository after the issue #828 split.
// Adding/removing methods here is a contract change that must be paired with
// an ADR amendment (ADR 202605222309).
var expectedUserRepoMethods = []string{
	"BumpAuthzEpoch",
	"Create",
	"Delete",
	"GetByID",
	"GetByIDForUpdate",
	"GetByUsername",
	"GetByUsernameForUpdate",
	"UpdateLockState",
	"UpdateLockoutFields",
	"UpdatePassword",
	"UpdatePasswordResetFlag",
	"UpdateProfile",
}

// TestUserRepoMethodSetFrozen verifies USERREPO-METHOD-SET-FROZEN-01.
func TestUserRepoMethodSetFrozen(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	iface := loadInterfaceType(t, root, "cells/accesscore/internal/ports", "UserRepository")
	if iface == nil {
		t.Fatal("USERREPO-METHOD-SET-FROZEN-01: UserRepository interface not found in cells/accesscore/internal/ports")
	}
	got := directMethodNames(iface)
	sort.Strings(got)

	want := append([]string(nil), expectedUserRepoMethods...)
	sort.Strings(want)

	if !equalStringSlices(got, want) {
		t.Errorf("USERREPO-METHOD-SET-FROZEN-01: UserRepository methods = %v, want exactly %v",
			got, want)
	}
}
