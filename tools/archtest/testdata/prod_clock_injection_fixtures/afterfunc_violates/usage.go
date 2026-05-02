// Package afterfunc_violates verifies that time.AfterFunc is flagged: 1 violation expected.
package afterfunc_violates

import "time"

func schedule(d time.Duration, fn func()) *time.Timer {
	return time.AfterFunc(d, fn)
}
