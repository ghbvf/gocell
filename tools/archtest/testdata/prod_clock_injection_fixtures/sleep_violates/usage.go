// Package sleep_violates verifies that time.Sleep is flagged: 1 violation expected.
package sleep_violates

import "time"

func nap(d time.Duration) {
	time.Sleep(d)
}
