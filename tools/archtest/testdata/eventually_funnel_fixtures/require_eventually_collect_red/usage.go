//go:build archtest_fixture

// Package require_eventually_collect_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// bare require.EventuallyWithT call (collect-style callback variant). Same ban
// as Eventually — the EventuallyWithT variant differs only in the condition
// callback's CollectT parameter and is functionally equivalent for funnel
// purposes.
package require_eventually_collect_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func UseRequireEventuallyCollect(t *testing.T) {
	require.EventuallyWithT(t,
		func(c *assert.CollectT) {},
		time.Second, 5*time.Millisecond,
		"bare require.EventuallyWithT")
}
