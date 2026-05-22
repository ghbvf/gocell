//go:build archtest_fixture

// Package require_eventually_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// bare require.Eventually call. Must be routed through testwait.External or
// testwait.Deterministic.
package require_eventually_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func UseRequireEventually(t *testing.T) {
	require.Eventually(t, func() bool { return true }, // violation on this line
		time.Second, 5*time.Millisecond,
		"bare require.Eventually")
}
