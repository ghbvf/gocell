//go:build archtest_fixture

// Package assert_eventually_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// bare assert.Eventually call. Asserts that the ban covers the assert
// package as well as require.
package assert_eventually_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func UseAssertEventually(t *testing.T) {
	assert.Eventually(t, func() bool { return true }, // violation on this line
		time.Second, 5*time.Millisecond,
		"bare assert.Eventually")
}
