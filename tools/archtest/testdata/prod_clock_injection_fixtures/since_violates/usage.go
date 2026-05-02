// Package since_violates is a fixture for archtest negative case:
// verifies that time.Since is flagged: 1 violation expected.
package since_violates

import "time"

func elapsed(t time.Time) time.Duration {
	return time.Since(t)
}
