// Package tick_violates verifies that time.Tick (the leaky shortcut form)
// is flagged: 1 violation expected.
package tick_violates

import "time"

func leak(interval time.Duration) <-chan time.Time {
	return time.Tick(interval)
}
