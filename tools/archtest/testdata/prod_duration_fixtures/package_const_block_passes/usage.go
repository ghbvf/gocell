// Package package_const_block_passes verifies that a const block at package
// level (multiple consts) is fully compliant: no violation expected.
package package_const_block_passes

import "time"

const (
	defaultRetention = 7 * 24 * time.Hour
	defaultPoll      = 5 * time.Second
)

func retention() time.Duration { return defaultRetention }
func poll() time.Duration      { return defaultPoll }
