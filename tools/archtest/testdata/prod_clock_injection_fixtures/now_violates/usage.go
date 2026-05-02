// Package now_violates is a fixture for archtest negative case:
// verifies that time.Now is flagged: 1 violation expected.
package now_violates

import "time"

func currentTime() time.Time {
	return time.Now()
}
