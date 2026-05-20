//go:build archtest_fixture

// Package reason_cross_pkg_const_red is a RED fixture for
// TEST-POLLING-EXTERNAL-REASON-LITERAL-01: reason is a cross-package const
// referenced as a SelectorExpr (helper.ReasonConst). Even though the value
// resolves to a valid kebab-case string at types level, the literal-shape check
// requires *ast.BasicLit at the callsite — the reason must be visible inline
// without hopping to another package's declaration.
//
// This fixture proves that the "not BasicLit" rejection works for the
// SelectorExpr form (cross-pkg const), complementing reason_const_ident_red
// which covers the same-package Ident form.
package reason_cross_pkg_const_red

import (
	"testing"
	"time"

	helper "github.com/ghbvf/gocell/tools/archtest/testdata/testwait_external_fixtures/reason_cross_pkg_const_red_helper"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, helper.ReasonConst, // violation: cross-pkg SelectorExpr const
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"cross-pkg const reason")
}
