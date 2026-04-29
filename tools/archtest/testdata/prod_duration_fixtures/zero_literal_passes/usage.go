// Package zero_literal_passes verifies that the zero sentinel "0" is exempt:
// both return 0 and var x time.Duration = 0 must not trigger a violation.
package zero_literal_passes

import "time"

func noTimeout() time.Duration {
	return 0
}

var _ time.Duration = 0
