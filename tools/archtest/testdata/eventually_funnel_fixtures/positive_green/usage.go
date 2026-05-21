//go:build archtest_fixture

// Package positive_green is a GREEN fixture for TEST-EVENTUALLY-FUNNEL-01:
// polling routed through testwait.External (synchronous, const-literal
// reason). 0 violations expected.
package positive_green

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

func UseTestwait(t testing.TB) {
	testwait.External(t, "fixture-positive",
		func() bool { return true },
		time.Second, 5*time.Millisecond,
		"ok")
}
