// Package time_now_add_named_passes verifies that time.Now().Add(namedConst)
// is compliant: no literal in the Add argument means no violation.
package time_now_add_named_passes

import "time"

const defaultRetentionPeriod = 24 * time.Hour

func cutoff() time.Time {
	return time.Now().Add(defaultRetentionPeriod)
}
