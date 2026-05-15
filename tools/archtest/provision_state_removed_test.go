// INVARIANT: PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01

package archtest

import (
	"go/ast"
	"testing"
)

// PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01
//
// Claim: 以下 10 个标识符在 cells/accesscore/... 业务代码中永久禁止出现。
// 它们是为 bootstrap headless provision mode 设计的 pending 状态机抽象前提，
// 随 bootstrap mode 一起删除（ADR §D3 v2）。archtest 防止未来 PR 误恢复。
//
// 豁免：本文件自身的字符串字面量（黑名单定义）不触发。
var bannedProvisionIdentifiers = []string{
	"UserSourceBootstrap",
	"ValidAdminProvisionSource",
	"ProvisionState",
	"ProvisionStatePending",
	"ProvisionStateComplete",
	"MarkProvisionPending",
	"MarkProvisionComplete",
	"IsRecoverableProvisionOrphan",
	"OutcomeOrphanRecovered",
	"createUserOrRecover",
}

func TestProvisionStateAndUserSourceBootstrapRemoved(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"cells/accesscore"},
		IncludeTests(),
		ExcludeRels("tools/archtest/provision_state_removed_test.go"),
	)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			EachInSubtree[ast.Ident](file, func(ident *ast.Ident) {
				for _, banned := range bannedProvisionIdentifiers {
					if ident.Name == banned {
						ds = append(ds, Diagnostic{
							Rel:     p.Rel(file),
							Line:    p.Fset.Position(ident.Pos()).Line,
							Message: banned,
						})
					}
				}
			})
		}
		return ds
	})

	Report(t, "PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01", diags)
}
