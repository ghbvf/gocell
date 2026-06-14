//go:build archtest_fixture

// Package reason_const_ident_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is a package-level const identifier — even though it resolves to a
// const string at types level, the literal-shape check requires *ast.BasicLit
// at the callsite so the reason text is visible without hopping to the
// declaration.
package reason_const_ident_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

const reasonConst = "kebab-case-reason"

func UseExternal(t testing.TB) {
	testwait.External(t, reasonConst, // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"const ident reason")
}
