//go:build archtest_fixture

// Package reason_variable_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason argument is a runtime variable, not a const string literal.
package reason_variable_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	reason := "kebab-case-reason"
	testwait.External(t, reason, // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"runtime reason")
}
