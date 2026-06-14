//go:build archtest_fixture

// Package reason_concat_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is built by concatenation, not a single string literal.
package reason_concat_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

func UseExternal(t testing.TB, suffix string) {
	testwait.External(t, "prefix-"+suffix, // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"concat reason")
}
