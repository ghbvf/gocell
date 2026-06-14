//go:build archtest_fixture

// Package reason_sprintf_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is built by fmt.Sprintf, not a const string literal.
package reason_sprintf_red

import (
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, fmt.Sprintf("kebab-%d", 1), // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"sprintf reason")
}
