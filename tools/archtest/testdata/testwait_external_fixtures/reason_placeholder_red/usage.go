//go:build archtest_fixture

// Package reason_placeholder_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is a placeholder identifier (todo / fixme / tbd / xxx / placeholder / wip)
// which provides no descriptive information about the polling site.
package reason_placeholder_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, "todo-fill-later", // violation on this line
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"placeholder")
}
