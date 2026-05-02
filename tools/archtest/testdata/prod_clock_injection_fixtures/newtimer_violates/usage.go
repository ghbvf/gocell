// Package newtimer_violates is a fixture for archtest negative case:
// verifies that time.NewTimer is flagged: 1 violation expected.
package newtimer_violates

import "time"

func wait(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	<-t.C
}
