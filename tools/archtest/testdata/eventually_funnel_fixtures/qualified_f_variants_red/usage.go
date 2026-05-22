//go:build archtest_fixture

// Package qualified_f_variants_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// formatted variants Eventuallyf / EventuallyWithTf via qualified-ident form.
// Verifies the prefix predicate (isBannedEventuallyFuncName) catches *f
// variants on the existing Uses path — without it the *f sites would slip
// through the original enumerated banned set.
package qualified_f_variants_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func UseRequireEventuallyf(t *testing.T) {
	require.Eventuallyf(t, func() bool { return true }, // violation on this line
		time.Second, 5*time.Millisecond,
		"bare require.Eventuallyf %s", "fmt")
}

func UseAssertEventuallyWithTf(t *testing.T) {
	assert.EventuallyWithTf(t, // violation on this line
		func(c *assert.CollectT) {},
		time.Second, 5*time.Millisecond,
		"bare assert.EventuallyWithTf %s", "fmt")
}
