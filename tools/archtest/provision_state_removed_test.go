package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
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

	modRoot := findModuleRoot(t)
	scanDirs := []string{
		filepath.Join(modRoot, "cells", "accesscore"),
	}

	fset := token.NewFileSet()
	var violations []string

	for _, dir := range scanDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 豁免本文件自身
			if strings.Contains(path, "provision_state_removed_test.go") {
				return nil
			}
			f, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				for _, banned := range bannedProvisionIdentifiers {
					if ident.Name == banned {
						pos := fset.Position(ident.Pos())
						violations = append(violations, pos.String()+": "+banned)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("PROVISION-STATE-AND-USERSOURCE-BOOTSTRAP-REMOVED-01: %d 个被禁标识符仍在 cells/accesscore/ 中出现:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
