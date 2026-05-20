//go:build archtest_fixture

// Package positive_green is a GREEN fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// a direct call with a const kebab-case literal reason. 0 violations expected.
package positive_green

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, "kebab-case-reason",
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"ok")
}
