//go:build archtest_fixture

// Package assertions_method_call_red is a RED fixture for TEST-EVENTUALLY-FUNNEL-01:
// method-selector form via *Assertions (require.New(t).Eventually,
// assert.New(t).EventuallyWithTf). Verifies resolveSelectorCalleeFunc consults
// info.Selections for MethodVal callees — without it these sites would slip
// through the Uses-only callee resolver.
package assertions_method_call_red

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func UseRequireAssertionsEventually(t *testing.T) {
	require.New(t).Eventually(func() bool { return true }, // violation on this line
		time.Second, 5*time.Millisecond,
		"bare require.Assertions.Eventually")
}

func UseAssertAssertionsEventuallyWithTf(t *testing.T) {
	assert.New(t).EventuallyWithTf( // violation on this line
		func(c *assert.CollectT) {},
		time.Second, 5*time.Millisecond,
		"bare assert.Assertions.EventuallyWithTf %s", "fmt")
}
