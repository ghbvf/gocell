//go:build archtest_fixture

// Package reason_placeholder_red is a RED fixture for TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
// reason is a placeholder identifier (todo / fixme / tbd / xxx / placeholder /
// wip / hack / temp / test / dummy) which provides no descriptive information
// about the polling site.
package reason_placeholder_red

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

func UseExternal(t testing.TB) {
	testwait.External(t, "todo-fill-later", // violation: original placeholder
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"placeholder")
}

func UseExternalHack(t testing.TB) {
	testwait.External(t, "hack-fixme", // violation: extended denylist (hack)
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"hack placeholder")
}
