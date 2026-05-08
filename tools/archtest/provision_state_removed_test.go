package archtest

import (
	"go/ast"
	"go/parser"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
	scope := scanner.DirsScope(root, []string{"cells/accesscore"},
		scanner.IncludeTests(),
		scanner.ExcludeRels("tools/archtest/provision_state_removed_test.go"),
	)

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		ast.Inspect(fc.File, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, banned := range bannedProvisionIdentifiers {
				if ident.Name == banned {
					diags = append(diags, scanner.Diagnostic{
						Rel:     fc.Rel,
						Line:    fc.Fset.Position(ident.Pos()).Line,
						Message: banned,
					})
				}
			}
			return true
		})
	})

	scanner.Report(t, "PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01", diags)
}
