//go:build archtest_fixture

// Package assert_eventually_collect_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// bare assert.EventuallyWithT call (collect-style callback variant).
package assert_eventually_collect_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func UseAssertEventuallyCollect(t *testing.T) {
	assert.EventuallyWithT(t,
		func(c *assert.CollectT) {},
		time.Second, 5*time.Millisecond,
		"bare assert.EventuallyWithT")
}
